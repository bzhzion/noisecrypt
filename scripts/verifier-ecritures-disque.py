#!/usr/bin/env python3
"""Garde-fou : la politique de confidentialite doit decrire ce que NoiseCrypt ecrit et contacte.

Transcrit de bzhzion/beammeup et bzhzion/hublot, adapte au Go. Deux controles, tous deux
DERIVES du code et non declares a la main :

1. Chaque nom de fichier, dossier ou cle de registre ecrit en dur dans le code Go
   (`.noisecrypt`, `.ncrykey`, `noisecrypt-dl-*`, `NoiseCrypt.Container`...) doit apparaitre
   dans le texte visible de `web/privacy.html`.
2. L'inventaire des points d'ecriture, de reseau et de lancement de programme (`os.OpenFile`,
   `os.CreateTemp`, `registry.CreateKey`, `net.Listen`, `exec.Command`...) est compare a une
   reference versionnee, `scripts/ecritures-disque.json`. Un ecart ne dit pas que la politique
   est fausse, il dit qu'il faut la RELIRE : c'est un fil de declenchement, pas une preuve.
   Apres relecture, `--maj` enregistre le nouvel inventaire.

Pourquoi il existe : la politique de BeamMeUp a ete fausse deux fois, chaque fois parce
qu'elle avait ete ecrite de memoire, et la premiere erreur a ete relevee en public par un
moderateur winget. Celle de NoiseCrypt a ete demandee par le meme moderateur.

Limite assumee : il ne voit pas ce qu'un programme tiers ecrit de son cote (yt-dlp, FFmpeg,
le navigateur), et un chemin construit dynamiquement echappe au controle 1. C'est pourquoi
le controle 2 existe.
"""
import html
import json
import re
import sys
from pathlib import Path

RACINE = Path(__file__).resolve().parent.parent
SOURCES = [RACINE / "cmd", RACINE / "internal"]
POLITIQUE = RACINE / "web" / "privacy.html"
REFERENCE = Path(__file__).resolve().parent / "ecritures-disque.json"

MOTIFS = [
    "os.WriteFile", "os.Create(", "os.CreateTemp", "os.MkdirTemp", "os.MkdirAll", "os.Mkdir(",
    "os.OpenFile", "os.Rename", "os.Chtimes", "os.Chmod", "os.UserHomeDir", "os.UserConfigDir",
    "os.UserCacheDir", "os.TempDir", "registry.CreateKey", "registry.DeleteKey", "icacls",
    "exec.Command", "net.Listen", "net.Lookup", "net.Dial", "http.Get", "http.Client",
    "ParseMultipartForm", "SetCookie",
]

# Les litteraux qui designent un nom sur le disque ou dans le registre :
# - un nom avec une extension connue (`identity.ncrykey`, `noisecrypt-*.mp4`) ;
# - une extension ou un dossier cache seul (`.ncrykey`, `.noisecrypt`) ;
# - un prefixe de fichier temporaire (`noisecrypt-dl-*`) ;
# - une cle de registre de l'integration a l'explorateur.
NOMS = [
    re.compile(r'"([A-Za-z0-9_%*\-]+\.(?:mp4|ncry|ncrykey|ncrypub|json|log|txt|png))"'),
    re.compile(r'"(\.(?:noisecrypt|ncry|ncrykey|ncrypub))"'),
    re.compile(r'"(noisecrypt-[A-Za-z0-9_\-]*\*?[A-Za-z0-9_.\-]*)"'),
    re.compile(r'"(NoiseCrypt[A-Za-z.]+)"|`[^`]*\\(NoiseCrypt[A-Za-z]+)`'),
]


def fichiers_go():
    for dossier in SOURCES:
        for f in sorted(dossier.rglob("*.go")):
            if not f.name.endswith("_test.go"):
                yield f


def lignes_de_code(chemin: Path):
    """Lignes du fichier hors commentaires : un commentaire qui cite un motif ne l'execute pas."""
    for ligne in chemin.read_text(encoding="utf-8").splitlines():
        if not ligne.strip().startswith("//"):
            yield ligne


def inventaire():
    resultat = {}
    for fichier in fichiers_go():
        compte = {}
        for ligne in lignes_de_code(fichier):
            for motif in MOTIFS:
                if motif in ligne:
                    compte[motif] = compte.get(motif, 0) + 1
        if compte:
            resultat[fichier.relative_to(RACINE).as_posix()] = compte
    return resultat


def noms_de_fichiers():
    noms = set()
    for fichier in fichiers_go():
        for ligne in lignes_de_code(fichier):
            for motif in NOMS:
                for trouve in motif.findall(ligne):
                    for nom in (trouve if isinstance(trouve, tuple) else (trouve,)):
                        if nom:
                            noms.add(nom)
    return noms


def fragments(nom: str):
    """`noisecrypt-*.mp4` -> ['noisecrypt-', '.mp4'] : chaque morceau fixe doit etre cite."""
    return [f for f in re.split(r"\*|%[a-z]", nom) if len(f) > 1]


def texte_visible(page: str) -> str:
    """Le texte du corps de la page, sans balises ni scripts.

    Comparer a la page entiere laisserait un fragment generique (`.mp4`, `noisecrypt-`) etre
    « cite » par du balisage de l'en-tete : meme correction que dans bzhzion/hublot.
    """
    corps = re.search(r"<body[^>]*>(.*)</body>", page, re.S)
    texte = corps.group(1) if corps else page
    texte = re.sub(r"<(script|style)[^>]*>.*?</\1>", " ", texte, flags=re.S)
    return html.unescape(re.sub(r"<[^>]+>", " ", texte))


def main() -> int:
    # Sans cela, la console Windows rend les guillemets francais en caracteres de remplacement.
    sys.stdout.reconfigure(encoding="utf-8")
    if not POLITIQUE.exists():
        print(f"ECRITURES DISQUE : {POLITIQUE.relative_to(RACINE)} n'existe pas.")
        return 1
    politique = texte_visible(POLITIQUE.read_text(encoding="utf-8"))
    erreurs = []

    noms = noms_de_fichiers()
    for nom in sorted(noms):
        manquants = [f for f in fragments(nom) if f not in politique]
        if manquants:
            erreurs.append(f"le code ecrit « {nom} » mais web/privacy.html ne cite pas {manquants}")

    courant = inventaire()
    if "--maj" in sys.argv:
        REFERENCE.write_text(json.dumps(courant, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(f"reference mise a jour : {REFERENCE.relative_to(RACINE)}")
    else:
        if not REFERENCE.exists():
            erreurs.append("pas de reference : lancer `python scripts/verifier-ecritures-disque.py --maj`")
        else:
            attendu = json.loads(REFERENCE.read_text(encoding="utf-8"))
            for fichier in sorted(set(attendu) | set(courant)):
                if attendu.get(fichier) != courant.get(fichier):
                    erreurs.append(
                        f"{fichier} : les points d'ecriture ont change (reference "
                        f"{attendu.get(fichier)}, code {courant.get(fichier)})"
                    )

    if erreurs:
        print("ECRITURES DISQUE : la politique de confidentialite est peut-etre devenue fausse.")
        for e in erreurs:
            print(f"  - {e}")
        print(
            "Relire web/privacy.html contre le code (stockage, reseau, desinstallation), la "
            "corriger si besoin, puis `python scripts/verifier-ecritures-disque.py --maj`."
        )
        return 1

    print(f"ecritures disque : conforme ({len(noms)} noms cites, "
          f"{sum(len(v) for v in courant.values())} motifs inventories).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
