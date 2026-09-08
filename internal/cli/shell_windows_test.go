//go:build windows

package cli

import (
	"strings"
	"testing"

	"github.com/bzhzion/noisecrypt/internal/keystore"
	"golang.org/x/sys/windows/registry"
)

// These run against a scratch registry root rather than the real one. A test that
// rewrites the developer's actual file associations is a test somebody disables, and then
// the mechanics go untested; a test that leaves half its keys behind on failure is worse.
func scratchRoot(t *testing.T) {
	t.Helper()
	previous := shellRoot
	shellRoot = `Software\Classes\NoiseCryptScratch`
	t.Cleanup(func() {
		_ = shellUnregister(&Env{Stdout: discard{}, Stderr: discard{}})
		// The scratch parent itself, which unregister has no reason to know about.
		for _, k := range []string{
			shellRoot + `\*\shell`, shellRoot + `\*`,
			shellRoot + `\Directory\Background\shell`, shellRoot + `\Directory\Background`,
			shellRoot + `\Directory`, shellRoot,
		} {
			_ = registry.DeleteKey(registry.CURRENT_USER, k)
		}
		shellRoot = previous
	})
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func TestShellRegisterWritesEveryEntry(t *testing.T) {
	scratchRoot(t)

	if _, _, code := run("shell", "register"); code != 0 {
		t.Fatalf("shell register exited %d", code)
	}

	cases := []struct {
		key, wants string
	}{
		// Right-click on any file. The `*` is a literal key name here and not a
		// wildcard, which is worth pinning: a check written with a wildcard-aware
		// helper reports this key missing when it is present, and that is how the
		// first manual verification of this feature went.
		// `-to-self` fait partie du contrat : sans lui le clic droit scelle sous une
		// phrase de passe, et le double-clic qui rouvre en redemande une, alors que
		// l'identite de la machine est juste la. Epingle pour qu'un retrait se voie.
		{encryptKey() + `\command`, ` -pause seal -in "%1" -to-self`},
		// Double-click a .ncry.
		{decryptKey() + `\shell\open\command`, ` -pause open -in "%1"`},
		// Right-click the background of a folder. L'identite doit etre creee DANS ce
		// dossier : l'entree lancait `keygen` sans argument, donc ecrivait a
		// l'emplacement par defaut en ignorant le dossier ou l'on venait de cliquer.
		// Un menu contextuel dont l'effet ne depend pas de son contexte.
		{identityKey() + `\command`, ` -pause keygen -out "%V\` + keystore.IdentityBaseName() + `"`},
	}
	for _, c := range cases {
		k, err := registry.OpenKey(registry.CURRENT_USER, c.key, registry.QUERY_VALUE)
		if err != nil {
			t.Errorf("%s was not created: %v", c.key, err)
			continue
		}
		command, _, err := k.GetStringValue("")
		k.Close()
		if err != nil {
			t.Errorf("%s has no command: %v", c.key, err)
			continue
		}
		if !strings.HasSuffix(command, c.wants) {
			t.Errorf("%s is %q, expected it to end with %q", c.key, command, c.wants)
		}
		// Every entry has to ask for the pause, or the console closes before the
		// result can be read and the whole gesture looks broken.
		if !strings.Contains(command, "-pause") {
			t.Errorf("%s does not pause, so its window will vanish", c.key)
		}
	}

	// The ProgID needs DefaultIcon, or the containers themselves stay blank pages in
	// Explorer however good the icons on the menu verbs are. Checked separately from the
	// commands above because it is a different key and a different failure: the gesture
	// works, the file just looks like nothing.
	icon, err := registry.OpenKey(registry.CURRENT_USER, decryptKey()+`\DefaultIcon`, registry.QUERY_VALUE)
	if err != nil {
		t.Errorf("the file type has no icon: %v", err)
	} else {
		got, _, _ := icon.GetStringValue("")
		icon.Close()
		// The document icon specifically, not index 0 which is the application tile.
		// Asserted on the exact index because the wrong one fails silently: Windows
		// renders whatever icon is there and every container ends up wearing the
		// program's face, with nothing anywhere reporting a problem.
		if !strings.HasSuffix(got, ","+containerIconIndex) {
			t.Errorf("DefaultIcon is %q, expected it to end with the container icon "+
				"index %q", got, containerIconIndex)
		}
		if strings.HasSuffix(got, ",0") {
			t.Error("DefaultIcon points at index 0, which is the application tile: " +
				"containers would look like copies of the program")
		}
	}

	// ⚠️ %V et non %1, et cette assertion vaut plus que le suffixe teste ci-dessus.
	// Pour un verbe pose sur Directory\Background\shell, %1 est VIDE : le programme
	// recevrait alors un chemin tronque et ecrirait l'identite n'importe ou, sans que
	// rien ne le signale. Les deux jetons se ressemblent assez pour qu'une relecture
	// humaine ne fasse pas la difference.
	if k, err := registry.OpenKey(registry.CURRENT_USER, identityKey()+`\command`, registry.QUERY_VALUE); err == nil {
		commande, _, _ := k.GetStringValue("")
		k.Close()
		if !strings.Contains(commande, `%V`) {
			t.Errorf("l'entree de dossier ne transmet pas le dossier : %q", commande)
		}
		if strings.Contains(commande, `%1`) {
			t.Errorf("l'entree de dossier utilise %%1, qui est vide sur un fond de dossier : %q", commande)
		}
	}

	// The extension has to point at the ProgID, or double-clicking does nothing at all.
	ext, err := registry.OpenKey(registry.CURRENT_USER, extensionKey(), registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("the .ncry association was not created: %v", err)
	}
	defer ext.Close()
	if got, _, _ := ext.GetStringValue(""); got != progID {
		t.Errorf("the extension points at %q rather than %q", got, progID)
	}
}

// ⚠️ Le defaut rapporte par painteau : un clic droit sur un `.ncry` ne proposait QUE de le
// rechiffrer. Deux causes, et il fallait les deux pour produire ce menu.
//
// D'une part le verbe de dechiffrement n'avait pas de libelle. Il existait bien, comme
// action par defaut du type, mais Windows affiche alors « Ouvrir », un mot qui ne dit pas
// que le fichier va etre dechiffre. D'autre part le verbe de chiffrement est pose sur `*`,
// donc sur tous les fichiers, conteneurs compris. Entre un « Ouvrir » muet et un « Encrypt
// with NoiseCrypt » explicite, le menu semblait n'offrir que le rechiffrement, sur le seul
// fichier pour lequel ca n'a aucun sens.
func TestMenuDUnConteneurProposeDeDechiffrer(t *testing.T) {
	scratchRoot(t)

	if _, _, code := run("shell", "register"); code != 0 {
		t.Fatal("shell register a echoue")
	}

	// Le verbe de dechiffrement doit porter un nom, et ce nom doit dire ce qu'il fait.
	k, err := registry.OpenKey(registry.CURRENT_USER, decryptKey()+`\shell\open`, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("le verbe d'ouverture n'existe pas : %v", err)
	}
	libelle, _, err := k.GetStringValue("")
	k.Close()
	if err != nil || libelle == "" {
		t.Fatalf("le verbe de dechiffrement n'a pas de libelle, Windows affichera "+
			"« Ouvrir » : %v", err)
	}
	if !strings.Contains(libelle, "Decrypt") {
		t.Errorf("le libelle est %q, il ne dit pas qu'il dechiffre", libelle)
	}

	// Et le chiffrement doit s'exclure des conteneurs, sinon il reste la seule entree
	// explicite du menu d'un `.ncry`.
	e, err := registry.OpenKey(registry.CURRENT_USER, encryptKey(), registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("le verbe de chiffrement n'existe pas : %v", err)
	}
	applique, _, err := e.GetStringValue("AppliesTo")
	e.Close()
	if err != nil {
		t.Fatalf("le verbe de chiffrement n'a pas d'AppliesTo, il s'affiche donc aussi "+
			"sur les .ncry : %v", err)
	}
	if !strings.Contains(applique, containerExt) {
		t.Errorf("AppliesTo vaut %q et ne mentionne pas %s", applique, containerExt)
	}
	// La negation est le coeur de la clause : sans elle, la requete restreindrait le
	// chiffrement aux SEULS conteneurs, soit exactement l'inverse.
	if !strings.Contains(applique, "NOT") {
		t.Errorf("AppliesTo vaut %q, sans negation il restreint au lieu d'exclure", applique)
	}
}

// Uninstalling has to leave nothing. A stale key pointing at a program that no longer
// exists gives a menu entry that silently does nothing, which is worse than no entry.
func TestShellUnregisterLeavesNothing(t *testing.T) {
	scratchRoot(t)

	if _, _, code := run("shell", "register"); code != 0 {
		t.Fatal("shell register failed")
	}
	if _, _, code := run("shell", "unregister"); code != 0 {
		t.Fatal("shell unregister failed")
	}

	for _, key := range []string{
		encryptKey(), decryptKey(), extensionKey(), identityKey(),
	} {
		if k, err := registry.OpenKey(registry.CURRENT_USER, key, registry.QUERY_VALUE); err == nil {
			k.Close()
			t.Errorf("%s survived unregistering", key)
		}
	}
}

// status is the only thing that can catch the one real weakness of registering a path:
// the binary moving. Nothing else notices, because Explorer just does nothing.
func TestShellStatusNoticesAMissingProgram(t *testing.T) {
	scratchRoot(t)

	stdout, _, _ := run("shell", "status")
	if !strings.Contains(stdout, "not registered") {
		t.Errorf("status on a clean root does not say so: %q", stdout)
	}

	if _, _, code := run("shell", "register"); code != 0 {
		t.Fatal("shell register failed")
	}
	stdout, _, _ = run("shell", "status")
	if !strings.Contains(stdout, "registered") {
		t.Error("status does not report a registration that exists")
	}

	// Point it at something that is not there and check it is noticed.
	k, _, err := registry.CreateKey(registry.CURRENT_USER, encryptKey()+`\command`, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	_ = k.SetStringValue("", `"C:\nowhere\noisecrypt.exe" -pause seal -in "%1"`)
	k.Close()

	stdout, _, _ = run("shell", "status")
	if !strings.Contains(stdout, "no longer exists") {
		t.Errorf("status did not notice the program is gone: %q", stdout)
	}
}

func TestPauseFlagIsStrippedBeforeDispatch(t *testing.T) {
	// The flag has to be invisible to the command behind it, or every command would
	// need to know about it.
	stdout, stderr, code := run("-pause", "version")
	if code != 0 {
		t.Fatalf("exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, Version) {
		t.Errorf("the command behind -pause did not run: %q", stdout)
	}

	// And on its own it is not a command.
	if _, _, code := run("-pause"); code != 2 {
		t.Errorf("-pause alone exited %d, expected usage", code)
	}
}
