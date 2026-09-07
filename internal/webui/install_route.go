package webui

import (
	"fmt"
	"net/http"
	"os"

	"github.com/bzhzion/noisecrypt/internal/crypt"
	"github.com/bzhzion/noisecrypt/internal/keystore"
)

// handleInstallIdentity genere une identite et l'ECRIT comme identite de la machine.
//
// # Pourquoi cette route renverse un choix ecrit
//
// `handleKeygen` porte le commentaire « the private half is returned once and never stored
// server side [...] so this process never writes a key to disk on its own initiative ».
// C'etait defendable quand l'interface n'etait qu'un bac a sable, et ca ne l'est plus :
// l'interface savait fabriquer une identite et pas l'installer, donc le seul chemin reel
// pour equiper la machine restait la ligne de commande. Une interface qui ne sait pas faire
// ce qu'on vient y chercher renvoie l'utilisateur ailleurs.
//
// Le principe est conserve la ou il compte : ce n'est plus « de sa propre initiative »,
// c'est sur une action nommee, distincte du simple `Generate`, precedee d'une confirmation
// qui donne le chemin exact. Le serveur n'ecoute que la boucle locale.
//
// # Le refus d'ecraser n'est pas negociable par defaut
//
// Remplacer une identite existante detruit l'acces a tout ce qui a ete chiffre pour elle,
// definitivement et sans recours. La route refuse donc, et il faut un `force` explicite,
// que l'interface n'envoie qu'apres une seconde confirmation.
func (s *Server) handleInstallIdentity(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		if err := r.ParseForm(); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
	}
	phrase := r.FormValue("passphrase")
	sansProtection := r.FormValue("noPassphrase") == "1"
	force := r.FormValue("force") == "1"

	if !sansProtection && phrase == "" {
		fail(w, http.StatusBadRequest, fmt.Errorf(
			"a passphrase is required, or tick the box to store the identity unprotected"))
		return
	}
	// Le meme plancher que le scellement, et pour la meme raison : le cout d'Argon2id
	// multiplie le prix d'une recherche, il ne cree pas d'espace a chercher.
	if !sansProtection && len(phrase) < crypt.MinPassphraseLength {
		fail(w, http.StatusBadRequest, fmt.Errorf(
			"passphrase is %d bytes, the minimum is %d", len(phrase),
			crypt.MinPassphraseLength))
		return
	}

	chemin, err := keystore.DefaultIdentityPath()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := os.Stat(chemin); err == nil && !force {
		fail(w, http.StatusConflict, fmt.Errorf(
			"an identity already exists at %s. Replacing it destroys access to "+
				"everything encrypted to it", chemin))
		return
	}

	id, err := crypt.GenerateIdentity()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}

	stocke := id.String()
	if !sansProtection {
		verrouille, err := crypt.LockIdentity(id, []byte(phrase), crypt.KDFParams{
			Time:   crypt.DefaultArgonTime,
			Memory: crypt.DefaultArgonMemory,
			Lanes:  crypt.DefaultArgonLanes,
		})
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		stocke = verrouille
	}

	if err := keystore.WriteIdentityFile(chemin, stocke, force); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	// La moitie publique dans le meme geste : sans elle la seule chose faite pour etre
	// partagee n'existerait qu'ici, dans une reponse HTTP que rien ne conserve.
	pub := keystore.PublicPathFor(chemin)
	if err := keystore.WriteIdentityFile(pub, id.Public.String(), true); err != nil {
		fail(w, http.StatusInternalServerError, fmt.Errorf(
			"the private identity was written to %s but its public half could not be: %w",
			chemin, err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"path":        chemin,
		"publicPath":  pub,
		"fingerprint": id.Public.Short(),
		"protected":   !sansProtection,
	})
}
