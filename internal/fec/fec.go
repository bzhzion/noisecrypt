// Package fec protects the sealed byte stream on its way through a video channel.
//
// # Why two layers
//
// A video channel damages data in two unrelated ways, and a single code cannot
// answer both well.
//
// Within a frame, blur, quantisation and deblocking flip individual cells. This is
// the common case, it affects almost every frame a little, and it is fatal to any
// scheme that treats a frame as all-or-nothing: if every frame has one bad cell out
// of two thousand, a frame-level code sees every frame as lost and recovers nothing.
//
// Across frames, rate conversion, cuts and re-timing drop whole frames. This is
// rarer but total, and a code that only works inside a frame cannot see it at all.
//
// So there are two layers. The intra-frame layer repairs cells so that a lightly
// damaged frame still yields a correct shard. The inter-frame layer repairs whole
// missing frames. Each layer's parity is spent on the failure mode it can actually
// fix, which is the entire reason for the split.
//
// # Erasures, not errors
//
// Reed-Solomon corrects twice as many erasures as errors, because an erasure comes
// with its position. The library used here reconstructs erasures only: it needs to
// be told which shards are missing, and cannot find a corrupted one on its own.
//
// That is where the soft demodulation in package modem pays off. Each shard carries
// a confidence, the minimum over the bytes that compose it, and the decoder erases
// the least confident shards first. Confidence is a good ranking and a bad oracle,
// though: a cell pushed well past a decision boundary lands near the neighbouring
// level and reports high confidence while being wrong. Measured, the least confident
// tenth of bytes held about a third of the corruption, roughly three and a half
// times better than chance.
//
// So the decoder never trusts confidence to decide whether it succeeded. It erases,
// reconstructs, and checks a CRC. If the CRC fails it erases one more shard and
// tries again, escalating until the parity runs out. The CRC is the only thing that
// says yes.
package fec

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"sort"

	"github.com/klauspost/reedsolomon"
)

// Version is the frame format version carried in every frame header.
const Version = 1

const (
	// headerSize is the size of one copy of the frame header.
	headerSize = 1 + 4 + 2 + 4 + 2 // version, block, shard, total length, checksum

	// headerCopies is how many times the header is repeated in each frame.
	//
	// The header cannot be protected by the intra-frame code, because reading it is
	// what tells the decoder which code parameters and which block the frame belongs
	// to. Three copies with a per-copy checksum and a byte-wise majority vote is the
	// cheapest thing that survives localised damage, and localised is what video
	// damage is: a scratch across one corner of a frame takes out one copy, not
	// three spread across it.
	headerCopies = 3

	// HeaderRegion is the total number of bytes the header occupies in a frame.
	HeaderRegion = headerSize * headerCopies

	// crcSize is the checksum prepended to each inter-frame shard.
	crcSize = 4

	// maxShards is the Reed-Solomon limit: the field is GF(256), so a codeword
	// cannot have more than 256 symbols.
	maxShards = 256
)

var (
	// ErrInvalidLayout is returned for parameters that cannot describe a working code.
	ErrInvalidLayout = errors.New("fec: invalid layout")

	// ErrTooDamaged is returned when a block has fewer surviving shards than it needs.
	ErrTooDamaged = errors.New("fec: too many frames lost to reconstruct")

	// ErrNoFrames is returned when decoding is given nothing usable.
	ErrNoFrames = errors.New("fec: no readable frames")
)

// Layout describes the two codes and the frame budget they have to fit in.
type Layout struct {
	// FrameBytes is the raw modulation capacity of one frame, header included.
	FrameBytes int

	// IntraData and IntraParity size the code inside a frame.
	IntraData, IntraParity int

	// InterData and InterParity size the code across frames.
	InterData, InterParity int
}

// NewLayout builds a layout, choosing the intra-frame granularity for you.
//
// Getting that granularity right is not obvious, and getting it wrong quietly
// destroys the codec. The first version of this package used eighteen sub-shards of
// twelve bytes, which looked reasonable and collapsed completely at a four percent
// byte error rate: with two hundred damaged bytes scattered across a frame, almost
// every one of the eighteen shards holds at least one, so the parity is exhausted
// long before the damage is.
//
// The fix is counter-intuitive at first and obvious afterwards. For sparse random
// errors, the number of *shards* hit is roughly the number of errors, capped by the
// shard count. So the fraction of shards damaged is (errors / shard count), and
// making shards smaller and more numerous drives that fraction down while leaving
// the payload size untouched. Eight errors across eighteen shards ruins forty-four
// percent of them; the same eight errors across two hundred and twenty-four shards
// ruin under four percent.
//
// So this wants the finest granularity the code allows, one shard per byte, up to the
// two hundred and fifty-six symbol ceiling of GF(256).
//
// # Why it is a search rather than one formula
//
// "Take 256 shards" was the first version and it silently discarded frame capacity.
// Every shard has to be the same size, so the bytes a layout can actually use are
// shardCount times shardSize, and the remainder up to the frame's capacity is thrown
// away. Taking the shard count first and deriving the size from it maximises the
// count, not the utilisation, and the two are not the same thing.
//
// Measured on real geometries when the social profile was being densified: 488 usable
// bytes became 256 shards of one byte, using 256 and discarding 232. Forty-eight
// percent of the frame, gone, for no benefit. At 1015 bytes it discarded a quarter.
//
// So this searches the shard sizes instead and keeps the one that wastes least,
// preferring the smaller size when two tie, because small shards are what buys the
// tolerance to scattered errors described above. 488 bytes then become 244 shards of
// two, using all of them.
func NewLayout(frameBytes int, intraParityRatio float64, interData, interParity int) (Layout, error) {
	available := frameBytes - HeaderRegion
	if available <= 1 {
		return Layout{}, fmt.Errorf("%w: %d frame bytes leave nothing after the %d byte header",
			ErrInvalidLayout, frameBytes, HeaderRegion)
	}
	if intraParityRatio <= 0 || intraParityRatio >= 1 {
		return Layout{}, fmt.Errorf("%w: intra parity ratio %.3f is outside (0, 1)",
			ErrInvalidLayout, intraParityRatio)
	}

	total := bestShardCount(available)
	parity := max(1, int(float64(total)*intraParityRatio+0.5))
	if parity >= total {
		return Layout{}, fmt.Errorf("%w: intra parity ratio %.3f leaves no data shards",
			ErrInvalidLayout, intraParityRatio)
	}

	l := Layout{
		FrameBytes:  frameBytes,
		IntraData:   total - parity,
		IntraParity: parity,
		InterData:   interData,
		InterParity: interParity,
	}
	if err := l.Validate(); err != nil {
		return Layout{}, err
	}
	return l, nil
}

// bestShardCount picks the intra-frame shard count that leaves the fewest frame bytes
// unused, preferring more shards when two counts waste the same amount.
func bestShardCount(available int) int {
	bestCount, bestUsed := 0, -1

	// Iterating shard sizes rather than counts, because the size is what determines
	// how much of the frame a layout can address.
	for size := 1; size <= available; size++ {
		count := min(maxShards, available/size)
		if count < 2 {
			// Below two shards there is no code left to build, and larger sizes
			// only make that worse.
			break
		}
		used := count * size
		if used > bestUsed || (used == bestUsed && count > bestCount) {
			bestCount, bestUsed = count, used
		}
	}

	if bestCount == 0 {
		return min(maxShards, available)
	}
	return bestCount
}

// Validate reports a layout that cannot work.
func (l Layout) Validate() error {
	switch {
	case l.IntraData <= 0 || l.IntraParity <= 0:
		return fmt.Errorf("%w: intra shards must be positive", ErrInvalidLayout)
	case l.InterData <= 0 || l.InterParity <= 0:
		return fmt.Errorf("%w: inter shards must be positive", ErrInvalidLayout)
	case l.IntraData+l.IntraParity > maxShards:
		return fmt.Errorf("%w: %d intra shards exceeds the %d symbol limit of GF(256)",
			ErrInvalidLayout, l.IntraData+l.IntraParity, maxShards)
	case l.InterData+l.InterParity > maxShards:
		return fmt.Errorf("%w: %d inter shards exceeds the %d symbol limit of GF(256)",
			ErrInvalidLayout, l.InterData+l.InterParity, maxShards)
	case l.IntraShardSize() <= 0:
		return fmt.Errorf("%w: %d frame bytes leave no room for %d intra shards after the %d byte header",
			ErrInvalidLayout, l.FrameBytes, l.IntraData+l.IntraParity, HeaderRegion)
	case l.ShardSize() <= 0:
		return fmt.Errorf("%w: no payload left after the intra checksum", ErrInvalidLayout)
	}
	return nil
}

// IntraShardSize is the size of one sub-shard inside a frame.
func (l Layout) IntraShardSize() int {
	total := l.IntraData + l.IntraParity
	if total <= 0 {
		return 0
	}
	return (l.FrameBytes - HeaderRegion) / total
}

// ShardSize is the number of payload bytes one frame carries for the inter-frame
// code, after the header, the intra parity and the checksum have taken their share.
func (l Layout) ShardSize() int {
	return l.IntraData*l.IntraShardSize() - crcSize
}

// BlockPayload is the number of payload bytes one block of frames carries.
func (l Layout) BlockPayload() int {
	return l.InterData * l.ShardSize()
}

// FramesPerBlock is how many frames one block produces.
func (l Layout) FramesPerBlock() int {
	return l.InterData + l.InterParity
}

// UsedFrameBytes is how many of the frame's bytes the layout actually occupies.
// The remainder is unused and gets filler, which is a rounding loss rather than a
// bug: shard sizes must divide evenly.
func (l Layout) UsedFrameBytes() int {
	return HeaderRegion + (l.IntraData+l.IntraParity)*l.IntraShardSize()
}

// Overhead is the ratio of frame bytes spent to payload bytes carried, minus one.
//
// This is derived rather than declared on purpose. A profile that states its own
// redundancy as a constant is stating an intention; this states what the layout
// actually costs, so a tuning change cannot quietly make the advertised figure a lie.
func (l Layout) Overhead() float64 {
	payload := l.ShardSize() * l.InterData
	if payload <= 0 {
		return 0
	}
	return float64(l.FrameBytes*l.FramesPerBlock())/float64(payload) - 1
}

// FrameCount returns how many frames are needed to carry n payload bytes.
func (l Layout) FrameCount(n int) int {
	blocks := max(1, (n+l.BlockPayload()-1)/l.BlockPayload())
	return blocks * l.FramesPerBlock()
}

// reedSolomonFor returns the intra-frame encoder for a layout.
func reedSolomonFor(l Layout) (reedsolomon.Encoder, error) {
	return reedsolomon.New(l.IntraData, l.IntraParity)
}

// Frame is one encoded frame, ready to be modulated.
type Frame struct {
	Block int
	Shard int
	Bytes []byte
}

// Encode turns a payload into frames.
func Encode(payload []byte, l Layout) ([]Frame, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	if len(payload) > int(^uint32(0)) {
		return nil, fmt.Errorf("%w: payload of %d bytes does not fit the 32-bit length field",
			ErrInvalidLayout, len(payload))
	}

	interEnc, err := reedsolomon.New(l.InterData, l.InterParity)
	if err != nil {
		return nil, fmt.Errorf("fec: inter-frame code: %w", err)
	}
	intraEnc, err := reedsolomon.New(l.IntraData, l.IntraParity)
	if err != nil {
		return nil, fmt.Errorf("fec: intra-frame code: %w", err)
	}

	shardSize := l.ShardSize()
	blockPayload := l.BlockPayload()
	blocks := max(1, (len(payload)+blockPayload-1)/blockPayload)
	totalLen := uint32(len(payload))

	frames := make([]Frame, 0, blocks*l.FramesPerBlock())

	for b := range blocks {
		// Build the inter-frame codeword for this block. The last block is padded
		// with zeros; the declared total length is what trims it on the way out.
		shards := make([][]byte, l.InterData+l.InterParity)
		for i := range shards {
			shards[i] = make([]byte, shardSize)
		}
		for i := range l.InterData {
			start := b*blockPayload + i*shardSize
			if start >= len(payload) {
				break
			}
			copy(shards[i], payload[start:min(start+shardSize, len(payload))])
		}
		if err := interEnc.Encode(shards); err != nil {
			return nil, fmt.Errorf("fec: encoding block %d: %w", b, err)
		}

		for s, shard := range shards {
			raw, err := buildFrame(shard, b, s, totalLen, l, intraEnc)
			if err != nil {
				return nil, err
			}
			frames = append(frames, Frame{Block: b, Shard: s, Bytes: raw})
		}
	}

	return frames, nil
}

func buildFrame(shard []byte, block, shardIdx int, totalLen uint32, l Layout, intraEnc reedsolomon.Encoder) ([]byte, error) {
	intraShardSize := l.IntraShardSize()

	// The frame's data region is the checksum followed by the inter-frame shard.
	// The checksum covers the shard only, so it stays meaningful after the intra
	// code has reconstructed it.
	data := make([]byte, l.IntraData*intraShardSize)
	binary.BigEndian.PutUint32(data[:crcSize], crc32.ChecksumIEEE(shard))
	copy(data[crcSize:], shard)

	sub := make([][]byte, l.IntraData+l.IntraParity)
	for i := range l.IntraData {
		sub[i] = data[i*intraShardSize : (i+1)*intraShardSize]
	}
	for i := l.IntraData; i < len(sub); i++ {
		sub[i] = make([]byte, intraShardSize)
	}
	if err := intraEnc.Encode(sub); err != nil {
		return nil, fmt.Errorf("fec: encoding frame %d/%d: %w", block, shardIdx, err)
	}

	out := make([]byte, l.FrameBytes)
	writeHeader(out, block, shardIdx, totalLen)
	off := HeaderRegion
	for _, s := range sub {
		copy(out[off:], s)
		off += intraShardSize
	}
	// Bytes past UsedFrameBytes stay zero here; the modem replaces the tail with
	// filler when it runs out of data, so they never reach the screen flat.
	return out, nil
}

func writeHeader(dst []byte, block, shard int, totalLen uint32) {
	var h [headerSize]byte
	h[0] = Version
	binary.BigEndian.PutUint32(h[1:5], uint32(block))
	binary.BigEndian.PutUint16(h[5:7], uint16(shard))
	binary.BigEndian.PutUint32(h[7:11], totalLen)
	binary.BigEndian.PutUint16(h[11:13], headerChecksum(h[:11]))

	for i := range headerCopies {
		copy(dst[i*headerSize:], h[:])
	}
}

// headerChecksum is the low half of CRC-32 over the header body. Sixteen bits are
// enough here: the header is eleven bytes and a wrong one is caught again by the
// payload checksum a moment later.
func headerChecksum(b []byte) uint16 {
	return uint16(crc32.ChecksumIEEE(b) & 0xFFFF)
}

type header struct {
	block    int
	shard    int
	totalLen uint32
}

// parseHeader recovers the header from its three copies.
//
// Each copy is tried on its own first, because one intact copy is the common case
// and majority voting a byte that is already right can only make it wrong. Only if
// all three fail their checksum does it fall back to a byte-wise majority, which can
// rebuild a header from three copies that are each individually damaged in different
// places.
func parseHeader(frame []byte) (header, bool) {
	if len(frame) < HeaderRegion {
		return header{}, false
	}

	for i := range headerCopies {
		if h, ok := decodeHeaderCopy(frame[i*headerSize : (i+1)*headerSize]); ok {
			return h, true
		}
	}

	var voted [headerSize]byte
	for b := range headerSize {
		var counts [256]uint8
		best, bestCount := byte(0), uint8(0)
		for i := range headerCopies {
			v := frame[i*headerSize+b]
			counts[v]++
			if counts[v] > bestCount {
				best, bestCount = v, counts[v]
			}
		}
		voted[b] = best
	}
	return decodeHeaderCopy(voted[:])
}

func decodeHeaderCopy(b []byte) (header, bool) {
	if len(b) != headerSize || b[0] != Version {
		return header{}, false
	}
	if binary.BigEndian.Uint16(b[11:13]) != headerChecksum(b[:11]) {
		return header{}, false
	}
	return header{
		block:    int(binary.BigEndian.Uint32(b[1:5])),
		shard:    int(binary.BigEndian.Uint16(b[5:7])),
		totalLen: binary.BigEndian.Uint32(b[7:11]),
	}, true
}

// ReadFrame is one frame as it came back from the channel: the demodulated bytes
// and the per-byte confidence from package modem.
//
// Confidence may be nil, in which case every byte is treated as equally trustworthy
// and the decoder falls back to erasing shards in order. That is strictly worse and
// exists so a caller without a soft demodulator can still decode.
type ReadFrame struct {
	Bytes      []byte
	Confidence []float64

	// Speculative marks a second reading of a frame that has already been offered,
	// sampled from a different rectangle because the first one looked wrong.
	//
	// It changes nothing about how the frame is decoded: it is checked by the same
	// CRC as any other, and the CRC decides. It only keeps the loss counters honest.
	// A speculative reading that fails is not a lost frame, it is a guess that did not
	// pay off, and counting it would make the figure that measures channel damage move
	// for reasons that have nothing to do with the channel.
	Speculative bool
}

// Stats records what a decode had to throw away.
//
// It exists because the number that was supposed to say how much margin was left was
// quietly optimistic. The decoder counted frames it could not *locate*, and dropped
// frames it located but could not *repair* without counting them anywhere, so a video
// that lost several frames could be reported as losing none. Redundancy absorbing damage
// silently is the system working; a report that hides it is the report failing.
type Stats struct {
	// Accepted is the number of frames that produced a verified shard.
	Accepted int

	// Unparseable frames had no readable header: the frame was located, but what was
	// sampled from it was not a frame of this format.
	Unparseable int

	// Unrepairable frames had a header but no combination of erasures made the shard
	// pass its CRC. That is the honest cost of a mislocated or badly damaged frame.
	// The inter-frame code sees it as an erasure, which is the cheap kind of loss, so
	// this is a margin indicator rather than a failure.
	Unrepairable int

	// OutOfRange frames named a shard index the layout does not have, or disagreed
	// with the others about the payload length.
	OutOfRange int

	// Recovered counts frames that failed as read and then succeeded from a second,
	// differently framed reading of the same image.
	Recovered int
}

// Lost is every frame that contributed nothing, after second readings are taken into
// account. A frame recovered by re-reading it is not a loss.
func (s Stats) Lost() int {
	return max(0, s.Unparseable+s.Unrepairable+s.OutOfRange-s.Recovered)
}

// Decode reconstructs the payload from whatever frames survived.
//
// Frames may arrive in any order, with duplicates, with gaps, and with damage. A
// frame that cannot be repaired is dropped rather than reported as an error, because on
// this channel losing frames is the expected case. It is counted, though: see DecodeStats.
func Decode(frames []ReadFrame, l Layout) ([]byte, error) {
	payload, _, err := DecodeStats(frames, l)
	return payload, err
}

// DecodeStats is Decode, and also says what it discarded.
func DecodeStats(frames []ReadFrame, l Layout) ([]byte, Stats, error) {
	var stats Stats
	payload, err := decode(frames, l, &stats)
	return payload, stats, err
}

func decode(frames []ReadFrame, l Layout, stats *Stats) ([]byte, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}

	intraEnc, err := reedsolomon.New(l.IntraData, l.IntraParity)
	if err != nil {
		return nil, fmt.Errorf("fec: intra-frame code: %w", err)
	}
	interEnc, err := reedsolomon.New(l.InterData, l.InterParity)
	if err != nil {
		return nil, fmt.Errorf("fec: inter-frame code: %w", err)
	}

	// block index -> shard index -> shard bytes
	blocks := map[int]map[int][]byte{}
	var totalLen uint32
	sawLength := false

	for _, f := range frames {
		h, ok := parseHeader(f.Bytes)
		if !ok {
			if !f.Speculative {
				stats.Unparseable++
			}
			continue
		}
		shard, ok := recoverShard(f, l, intraEnc)
		if !ok {
			if !f.Speculative {
				stats.Unrepairable++
			}
			continue
		}
		if h.shard >= l.InterData+l.InterParity {
			if !f.Speculative {
				stats.OutOfRange++
			}
			continue
		}

		if !sawLength {
			totalLen, sawLength = h.totalLen, true
		} else if h.totalLen != totalLen {
			stats.OutOfRange++
			// Frames disagreeing on the total length means two different payloads
			// got mixed together. Trusting the first one seen would silently
			// truncate or over-read; skipping the odd frames out keeps the block
			// map consistent with the length we committed to.
			continue
		}

		if blocks[h.block] == nil {
			blocks[h.block] = map[int][]byte{}
		}
		if _, dup := blocks[h.block][h.shard]; !dup {
			blocks[h.block][h.shard] = shard
			stats.Accepted++
			if f.Speculative {
				// A speculative reading only exists because the ordinary one was
				// offered first and failed, so this cancels that loss rather than
				// adding to the tally beside it.
				stats.Recovered++
			}
		}
	}

	if !sawLength {
		return nil, ErrNoFrames
	}

	blockPayload := l.BlockPayload()
	wantBlocks := max(1, (int(totalLen)+blockPayload-1)/blockPayload)

	out := make([]byte, 0, int(totalLen))
	for b := range wantBlocks {
		present := blocks[b]
		if len(present) < l.InterData {
			return nil, fmt.Errorf("%w: block %d has %d of the %d frames it needs",
				ErrTooDamaged, b, len(present), l.InterData)
		}

		shards := make([][]byte, l.InterData+l.InterParity)
		for i := range shards {
			shards[i] = present[i] // nil when absent, which is what Reconstruct wants
		}
		if err := interEnc.ReconstructData(shards); err != nil {
			return nil, fmt.Errorf("fec: reconstructing block %d: %w", b, err)
		}
		for i := range l.InterData {
			out = append(out, shards[i]...)
		}
	}

	if len(out) < int(totalLen) {
		return nil, fmt.Errorf("%w: recovered %d bytes, header declares %d", ErrTooDamaged, len(out), totalLen)
	}
	return out[:totalLen], nil
}

// recoverShard repairs one frame's payload and verifies it.
//
// Exactly two attempts are made, and the reason there are not more is a small
// dominance argument worth writing down, because the obvious implementation walks
// the erasure count up one at a time and does fifty-six times the work for nothing.
//
// Reed-Solomon reconstruction succeeds precisely when every damaged shard has been
// erased and the erasure count does not exceed the parity. The shards are erased in
// confidence order, so the set erased at count k is a prefix of the set erased at
// count k+1. If reconstruction succeeds at some k below the parity budget, then all
// the damaged shards sit inside that prefix, so they also sit inside the full
// budget's prefix, so erasing the full budget succeeds too. Erasing a healthy shard
// costs nothing: the code rebuilds it exactly.
//
// So erasing the whole parity budget dominates every smaller count, and the only
// other attempt worth making is zero, which skips reconstruction entirely for the
// common case of an undamaged frame.
//
// The CRC decides both times. Confidence chooses what to erase; it never gets to
// say whether the result is right.
func recoverShard(f ReadFrame, l Layout, enc reedsolomon.Encoder) ([]byte, bool) {
	intraShardSize := l.IntraShardSize()
	total := l.IntraData + l.IntraParity
	if len(f.Bytes) < HeaderRegion+total*intraShardSize {
		return nil, false
	}
	region := f.Bytes[HeaderRegion : HeaderRegion+total*intraShardSize]

	order := shardsByConfidence(f.Confidence, l)

	for _, erase := range [2]int{0, l.IntraParity} {
		shards := make([][]byte, total)
		for i := range total {
			s := make([]byte, intraShardSize)
			copy(s, region[i*intraShardSize:(i+1)*intraShardSize])
			shards[i] = s
		}
		for _, idx := range order[:erase] {
			shards[idx] = nil
		}

		if erase > 0 {
			if err := enc.ReconstructData(shards); err != nil {
				continue
			}
		}

		data := make([]byte, 0, l.IntraData*intraShardSize)
		for i := range l.IntraData {
			data = append(data, shards[i]...)
		}
		shard := data[crcSize:]
		if binary.BigEndian.Uint32(data[:crcSize]) == crc32.ChecksumIEEE(shard) {
			return shard, true
		}
	}

	return nil, false
}

// shardsByConfidence orders sub-shard indices from least to most trustworthy.
//
// A sub-shard's confidence is the minimum over its bytes, for the same reason a
// byte's is the minimum over its cells: one bad byte spoils the shard, so an average
// would let a single coin flip hide behind its neighbours.
func shardsByConfidence(confidence []float64, l Layout) []int {
	total := l.IntraData + l.IntraParity
	intraShardSize := l.IntraShardSize()

	order := make([]int, total)
	for i := range order {
		order[i] = i
	}
	if len(confidence) < HeaderRegion+total*intraShardSize {
		// No usable confidence: fall back to erasing in order. Strictly worse, and
		// the CRC still decides, so this degrades rather than breaks.
		return order
	}

	scores := make([]float64, total)
	for i := range total {
		worst := 1.0
		base := HeaderRegion + i*intraShardSize
		for j := range intraShardSize {
			worst = min(worst, confidence[base+j])
		}
		scores[i] = worst
	}

	sort.SliceStable(order, func(a, b int) bool {
		return scores[order[a]] < scores[order[b]]
	})
	return order
}
