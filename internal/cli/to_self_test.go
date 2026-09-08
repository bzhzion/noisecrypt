package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bzhzion/noisecrypt/internal/keystore"
)

// Le geste demande par painteau : un clic droit « encrypt » doit sceller pour l'identite de
// cette machine, et le double-clic qui rouvre ne doit alors rien demander du tout, puisque
// `open` consulte deja cette identite sur un conteneur hybride.
//
// Avant, chiffrer partait directement sur une phrase de passe : il fallait en inventer une
// puis la retaper pour rouvrir, pour un fichier qu'on chiffre en general pour soi.
func TestToSelfScellePourLaMachineEtRouvreSansRienDemander(t *testing.T) {
	maison := t.TempDir()
	t.Setenv(keystore.StoreDirEnvVar, maison)

	if _, _, code := run("keygen", "-no-passphrase"); code != 0 {
		t.Fatal("keygen a echoue")
	}

	clair := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(clair, []byte("chiffre pour cette machine"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Aucune source de phrase de passe fournie, et c'est le point : si le scellement en
	// reclamait une, ce test echouerait ici.
	stdout, stderr, code := run("seal", "-in", clair, "-to-self")
	if code != 0 {
		t.Fatalf("seal -to-self a echoue (%d) : %s", code, stderr)
	}
	if !strings.Contains(stdout, "this machine's identity") {
		t.Errorf("le scellement ne dit pas pour qui il scelle :\n%s", stdout)
	}

	revenu := filepath.Join(t.TempDir(), "revenu.txt")
	if _, stderr, code := run("open", "-in", clair+".ncry", "-out", revenu); code != 0 {
		t.Fatalf("open a echoue sans identite explicite (%d) : %s", code, stderr)
	}
	b, err := os.ReadFile(revenu)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "chiffre pour cette machine" {
		t.Errorf("contenu revenu %q", b)
	}
}

// L'autre moitie de la demande, « et si echec demander » : sur une machine sans identite le
// scellement retombe sur une phrase de passe, en le DISANT. Le silence serait le vrai defaut
// ici, les deux conteneurs n'ayant pas la meme nature : l'un ne s'ouvre qu'avec l'identite,
// l'autre s'ouvre partout.
func TestToSelfSansIdentiteRetombeSurLaPhraseDePasseEtLeDit(t *testing.T) {
	t.Setenv(keystore.StoreDirEnvVar, t.TempDir()) // vide, aucun keygen
	t.Setenv("NC_TEST_PASS", "correcte-horse-battery")

	clair := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(clair, []byte("sans identite"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := run("seal", "-in", clair, "-to-self", "-passphrase-env", "NC_TEST_PASS")
	if code != 0 {
		t.Fatalf("le repli a echoue (%d) : %s", code, stderr)
	}
	if !strings.Contains(stderr, "No identity on this machine") {
		t.Errorf("le repli est silencieux, l'utilisateur ne sait pas quelle sorte de "+
			"conteneur il vient de fabriquer :\n%s", stderr)
	}

	// Et il s'ouvre bien par la phrase de passe, donc c'est reellement l'autre mode.
	revenu := filepath.Join(t.TempDir(), "revenu.txt")
	if _, stderr, code := run("open", "-in", clair+".ncry", "-out", revenu,
		"-passphrase-env", "NC_TEST_PASS"); code != 0 {
		t.Fatalf("le conteneur de repli ne s'ouvre pas par phrase de passe (%d) : %s", code, stderr)
	}
}

// ⚠️ La ligne de commande ne doit PAS changer de comportement. Sans `-to-self`, un `seal` nu
// continue de reclamer une phrase de passe : sceller pour la machine sans qu'on l'ait demande
// fabriquerait des conteneurs ouvrables sur un seul poste a la place de conteneurs ouvrables
// partout, et personne ne l'aurait su.
func TestSealNuNeScellePasPourLaMachine(t *testing.T) {
	maison := t.TempDir()
	t.Setenv(keystore.StoreDirEnvVar, maison)
	if _, _, code := run("keygen", "-no-passphrase"); code != 0 {
		t.Fatal("keygen a echoue")
	}

	clair := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(clair, []byte("sans -to-self"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Aucune phrase de passe fournie et pas de terminal : doit echouer, et pas sceller
	// silencieusement pour l'identite qui existe pourtant juste a cote.
	stdout, _, code := run("seal", "-in", clair)
	if code == 0 {
		t.Fatalf("un seal nu a reussi sans phrase de passe, il a donc scelle pour la "+
			"machine tout seul :\n%s", stdout)
	}
	if strings.Contains(stdout, "this machine's identity") {
		t.Errorf("un seal nu a scelle pour la machine :\n%s", stdout)
	}
}
