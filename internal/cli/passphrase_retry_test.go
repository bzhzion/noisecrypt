package cli

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// Le defaut rapporte le 2026-09-08 : clic droit « encrypt », l'invite s'affiche, une
// validation a vide, et la fenetre annonce « no passphrase supplied » puis se ferme. Aucune
// seconde chance, sur le verbe le plus utilise du programme.
//
// Reproduit ici a la lettre avant d'etre corrige : la premiere reponse est vide.
func TestPassphraseVideRedemandeAuLieuDAbandonner(t *testing.T) {
	terminalFactice(t)

	// Trois reponses et pas deux : en scellement `confirm` est vrai, donc une saisie
	// acceptee est suivie d'une demande de confirmation. Detail que la premiere version de
	// ce test avait manque, et c'est le test qui l'a appris.
	reponses := []string{"", "correcte-horse-battery", "correcte-horse-battery"}
	var invites int
	env := envFactice(&reponses, &invites)
	s := &passphraseSource{confirm: true}

	p, err := s.resolveInteractif(env, "Passphrase: ")
	if err != nil {
		t.Fatalf("une saisie vide suivie d'une bonne a echoue : %v", err)
	}
	if string(p) != "correcte-horse-battery" {
		t.Errorf("phrase retenue %q", p)
	}
	if invites != 3 {
		t.Errorf("%d invites, on en attend 3 : la vide, la bonne, sa confirmation", invites)
	}
}

// Le plancher de longueur se corrige aussi en retapant. Teste separement de la saisie vide
// parce que ce sont deux erreurs distinctes et qu'une seule des deux etait sentinelle : le
// message du plancher etait un fmt.Errorf nu, donc indistinguable d'une panne de lecture.
func TestPassphraseTropCourteRedemande(t *testing.T) {
	terminalFactice(t)

	reponses := []string{"court", "assez-longue-pour-le-plancher", "assez-longue-pour-le-plancher"}
	var invites int
	env := envFactice(&reponses, &invites)
	s := &passphraseSource{confirm: true}

	p, err := s.resolveInteractif(env, "Passphrase: ")
	if err != nil {
		t.Fatalf("une saisie trop courte suivie d'une bonne a echoue : %v", err)
	}
	if string(p) != "assez-longue-pour-le-plancher" {
		t.Errorf("phrase retenue %q", p)
	}
	if invites != 3 {
		t.Errorf("%d invites, on en attend 3 : la courte, la bonne, sa confirmation", invites)
	}
}

// ⚠️ Le garde-fou qui compte le plus, et la raison de la sentinelle ErrPassphraseTooShort :
// une lecture qui echoue parce qu'il n'y a personne NE DOIT PAS etre redemandee. Sans cette
// distinction la boucle est une attente infinie sans rien pour l'interrompre, ce qui sur une
// fenetre lancee depuis l'Explorateur est pire que le defaut d'origine.
func TestLectureImpossibleNeBouclePas(t *testing.T) {
	terminalFactice(t)

	var invites int
	env := &Env{
		Stdout: io.Discard, Stderr: io.Discard,
		Interactive: true,
		ReadPassphrase: func(string) ([]byte, error) {
			invites++
			if invites > 3 {
				t.Fatal("resolveInteractif boucle sur une erreur de lecture")
			}
			return nil, io.EOF
		},
	}
	s := &passphraseSource{confirm: true}

	if _, err := s.resolveInteractif(env, "Passphrase: "); !errors.Is(err, io.EOF) {
		t.Fatalf("erreur rendue %v, on attend l'erreur de lecture telle quelle", err)
	}
	if invites != 1 {
		t.Errorf("%d invites, on en attend exactement 1", invites)
	}
}

// Hors mode interactif, redemander serait une boucle dans un script. L'erreur d'origine est
// alors la bonne reponse, et c'est le comportement d'avant le correctif.
func TestSansModeInteractifEchoueImmediatement(t *testing.T) {
	terminalFactice(t)

	reponses := []string{"", "correcte-horse-battery"}
	var invites int
	env := envFactice(&reponses, &invites)
	env.Interactive = false
	s := &passphraseSource{confirm: true}

	if _, err := s.resolveInteractif(env, "Passphrase: "); !errors.Is(err, ErrNoPassphrase) {
		t.Fatalf("erreur rendue %v, on attend ErrNoPassphrase", err)
	}
	if invites != 1 {
		t.Errorf("%d invites, on en attend 1 : rien ne doit etre redemande sans personne", invites)
	}
}

// Le plancher doit rester une sentinelle reconnaissable, sinon resolveInteractif ne peut pas
// distinguer une saisie corrigeable d'une panne. Epingle a part parce que c'est une
// propriete du message d'erreur, que rien d'autre ne verifie.
func TestPlancherEstUneSentinelle(t *testing.T) {
	s := &passphraseSource{confirm: true}
	_, err := s.accept([]byte("court"))
	if !errors.Is(err, ErrPassphraseTooShort) {
		t.Fatalf("le plancher rend %v, qui n'enveloppe pas ErrPassphraseTooShort", err)
	}
	// Le detail chiffre doit survivre a l'enveloppement : c'est lui qui dit quoi corriger.
	if !strings.Contains(err.Error(), "5 bytes") {
		t.Errorf("le message ne dit plus la longueur fournie : %v", err)
	}
}

func terminalFactice(t *testing.T) {
	t.Helper()
	precedent := stdinIsTerminal
	stdinIsTerminal = func() bool { return true }
	t.Cleanup(func() { stdinIsTerminal = precedent })
}

func envFactice(reponses *[]string, invites *int) *Env {
	return &Env{
		Stdout: io.Discard, Stderr: io.Discard,
		Interactive: true,
		ReadPassphrase: func(string) ([]byte, error) {
			*invites++
			if len(*reponses) == 0 {
				return nil, io.EOF
			}
			r := (*reponses)[0]
			*reponses = (*reponses)[1:]
			return []byte(r), nil
		},
	}
}
