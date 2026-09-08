package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Le logo du site vitrine et l'icone de l'application doivent rester le meme dessin.
//
// Ils ne l'etaient pas : le site portait un motif de cellules ecrit a la main dans son HTML,
// cense evoquer le produit, alors que l'icone de l'application est un N. Deux dessins pour
// une seule marque, dont un qui ne representait rien. Le SVG sort maintenant de la meme
// matrice, et ce test echoue si le fichier commite cesse d'etre celui que le generateur
// produit, que ce soit par une retouche a la main ou par un oubli de regeneration apres une
// refonte de l'icone.
func TestLeSVGDuSiteEstCeluiQueLeGenerateurProduit(t *testing.T) {
	commite := filepath.Join("..", "..", "web", "favicon.svg")
	attendu, err := os.ReadFile(commite)
	if err != nil {
		t.Fatalf("le SVG du site est introuvable : %v", err)
	}

	frais := filepath.Join(t.TempDir(), "favicon.svg")
	writeTileSVG(frais)
	obtenu, err := os.ReadFile(frais)
	if err != nil {
		t.Fatal(err)
	}

	if string(obtenu) != string(attendu) {
		t.Errorf("web/favicon.svg ne correspond plus au generateur.\n"+
			"Relancer `go run ./tools/icongen` et committer le resultat.\n"+
			"commite : %d octets\nproduit : %d octets", len(attendu), len(obtenu))
	}
}

// Et la marque doit rester reconnaissable : un N, pas du bruit. Le test porte sur la
// matrice plutot que sur le rendu, parce que c'est elle qui decide, et parce qu'un
// aplatissement accidentel en motif aleatoire passerait toute verification de taille.
func TestLaTuileDessineBienUnN(t *testing.T) {
	// Les deux montants verticaux, pleins sur toute la hauteur.
	for row := range tileCells {
		if tileShape[row][0] != 1 {
			t.Errorf("le montant gauche est troue en rangee %d", row)
		}
		if tileShape[row][tileCells-1] != 1 {
			t.Errorf("le montant droit est troue en rangee %d", row)
		}
	}
	// Et la diagonale qui les relie, celle qui porte la couleur de signal.
	for i := range tileCells {
		if tileShape[i][i] != 1 {
			t.Errorf("la diagonale est trouee en %d", i)
		}
		if tileColour(i, i) != signal {
			t.Errorf("la cellule diagonale %d n'est pas en couleur de signal", i)
		}
	}
	if tileColour(0, tileCells-1) != paper {
		t.Error("une cellule hors diagonale porte la couleur de signal")
	}
}
