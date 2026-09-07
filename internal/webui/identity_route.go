package webui

import (
	"net/http"
	"os"
	"strings"

	"github.com/bzhzion/noisecrypt/internal/crypt"
	"github.com/bzhzion/noisecrypt/internal/keystore"
)

// etatIdentite decrit ce qui existe sur cette machine, pour que l'interface puisse le
// DIRE au lieu de presenter un champ vide.
//
// Le point qui rend cette route possible sans rien demander a l'utilisateur : l'empreinte
// est lue dans le fichier `.ncrypub`, qui n'est pas chiffre. La deriver de la cle privee
// exigerait la phrase de passe, donc afficher « voici l'identite de ce PC » couterait une
// saisie avant meme de savoir s'il y en a une. C'est une raison de plus d'ecrire cette
// moitie publique sur le disque.
type etatIdentite struct {
	Existe       bool   `json:"exists"`
	Protegee     bool   `json:"locked"`
	Chemin       string `json:"path"`
	Empreinte    string `json:"fingerprint"`
	CheminPublic string `json:"publicPath"`
	// Vrai quand la privee est la mais pas sa moitie publique, cas des identites creees
	// avant que ce fichier existe. L'interface peut alors dire quoi faire au lieu de
	// pretendre qu'il n'y a pas d'identite.
	PubliqueAbsente bool `json:"publicMissing"`
}

func (s *Server) handleIdentity(w http.ResponseWriter, r *http.Request) {
	var e etatIdentite

	chemin, err := keystore.DefaultIdentityPath()
	if err != nil {
		writeJSON(w, http.StatusOK, e)
		return
	}
	e.Chemin = chemin

	brut, err := os.ReadFile(chemin)
	if err != nil {
		writeJSON(w, http.StatusOK, e)
		return
	}
	e.Existe = true
	e.Protegee = crypt.IsLockedIdentity(strings.TrimSpace(string(brut)))

	pub := keystore.PublicPathFor(chemin)
	e.CheminPublic = pub
	brutPub, err := os.ReadFile(pub)
	if err != nil {
		e.PubliqueAbsente = true
		writeJSON(w, http.StatusOK, e)
		return
	}
	id, err := crypt.ParsePublicIdentity(strings.TrimSpace(string(brutPub)))
	if err != nil {
		e.PubliqueAbsente = true
		writeJSON(w, http.StatusOK, e)
		return
	}
	e.Empreinte = id.Short()
	writeJSON(w, http.StatusOK, e)
}
