package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bzhzion/noisecrypt/internal/keystore"
)

// Le defaut que ces deux tests gardent : la moitie publique de l'identite n'existait que
// dans la sortie de `keygen`, imprimee une fois dans une console qui se referme seule, et
// aucune commande ne permettait de la revoir. La cle privee etait intacte et l'utilisateur
// pouvait toujours dechiffrer ; il n'avait simplement plus aucun moyen de dire a quelqu'un
// comment chiffrer a son intention, sinon regenerer et perdre l'acces a tout ce qui avait
// deja ete scelle pour l'ancienne.
func TestKeygenWritesThePublicHalfBesideThePrivateOne(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NOISECRYPT_HOME", dir)
	t.Setenv("NC_TEST_PASS", "une phrase avec des espaces")

	if _, stderr, code := run("keygen", "-passphrase-env", "NC_TEST_PASS"); code != 0 {
		t.Fatalf("keygen a echoue: %s", stderr)
	}

	priv := filepath.Join(dir, "identity.ncrykey")
	pub := keystore.PublicPathFor(priv)
	for _, f := range []string{priv, pub} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("%s n'a pas ete ecrit: %v", f, err)
		}
	}

	// La moitie publique doit etre lisible SANS la phrase de passe : c'est ce qui permet
	// a l'interface d'annoncer l'identite du poste sans rien demander.
	contenu, err := os.ReadFile(pub)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(contenu)), "noisecrypt-public-v1:") {
		t.Errorf("le fichier public ne contient pas une identite publique: %.40s", contenu)
	}
	if strings.Contains(string(contenu), "noisecrypt-locked") {
		t.Error("le fichier public contient une identite VERROUILLEE, donc la privee")
	}
}

func TestIdentityRewritesAMissingPublicHalf(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NOISECRYPT_HOME", dir)
	t.Setenv("NC_TEST_PASS", "une phrase avec des espaces")

	if _, stderr, code := run("keygen", "-passphrase-env", "NC_TEST_PASS"); code != 0 {
		t.Fatalf("keygen a echoue: %s", stderr)
	}
	pub := keystore.PublicPathFor(filepath.Join(dir, "identity.ncrykey"))
	attendu, err := os.ReadFile(pub)
	if err != nil {
		t.Fatal(err)
	}

	// Le cas reel : une identite creee avant que ce fichier existe.
	if err := os.Remove(pub); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := run("identity", "-identity-passphrase-env", "NC_TEST_PASS")
	if code != 0 {
		t.Fatalf("identity a echoue: %s", stderr)
	}
	if !strings.Contains(stdout, "Fingerprint:") {
		t.Error("l'empreinte n'est pas affichee, or c'est la seule forme comparable a voix haute")
	}

	revenu, err := os.ReadFile(pub)
	if err != nil {
		t.Fatalf("la moitie publique n'a pas ete reecrite: %v", err)
	}
	if string(revenu) != string(attendu) {
		t.Error("la moitie publique reecrite differe de l'originale, alors qu'elle se " +
			"derive de la meme cle privee")
	}
}
