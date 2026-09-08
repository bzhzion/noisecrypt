# Site vitrine statique de NoiseCrypt, juste du HTML et du CSS : nginx sert les fichiers
# tels quels, pas d'etape de construction. Meme motif que ombra/web, hucheor/web,
# patelin/web et cabanon/web, transcrit et non reinvente.
FROM nginx:alpine
ARG GIT_SHA=dev
# La version n'est PAS ecrite dans la page. Elle vit dans les tags git, comme partout
# ailleurs dans ce parc, et la CI la resout puis la substitue ici. Un numero recopie a la
# main dans un fichier devient faux a la release suivante, sans que rien ne le signale :
# c'est exactement le defaut corrige deux fois dans ce depot le meme jour, sur la version
# de fichier de l'installateur puis sur celle du binaire.
ARG APP_VERSION=dev
COPY index.html styles.css favicon.svg robots.txt sitemap.xml /usr/share/nginx/html/
COPY nginx.conf /etc/nginx/conf.d/default.conf
RUN sed -i "s/{{VERSION}}/${APP_VERSION}/g" /usr/share/nginx/html/index.html
# Garde-fou : si le gabarit gagne un jour une occurrence que le sed ne remplace pas, la
# page publierait « {{VERSION}} » en clair. La construction echoue plutot que de servir ca.
RUN ! grep -q '{{VERSION}}' /usr/share/nginx/html/index.html
# La feuille de style porte elle aussi une empreinte, sans quoi un navigateur qui a deja
# visite le site continue de servir l'ancien styles.css apres un deploiement, et le site
# parait ne pas avoir change.
#
# ⚠️ Une empreinte de COMMIT, et jamais le numero de version : la convention du parc
# l'interdit explicitement, parce qu'un `?v=0.6.0` est lisible sur une page publique et
# renseignerait la version que la sonde de sante refuse justement de donner sans jeton.
RUN sed -i "s/styles\.css\"/styles.css?v=${GIT_SHA}\"/" /usr/share/nginx/html/index.html
# Et un garde-fou, parce qu'un `sed` qui ne trouve pas son motif rend 0 : renommer la
# feuille ou reformater cette balise ferait disparaitre le cache-busting en silence, et
# personne ne le verrait avant un deploiement qui ne se voit pas.
RUN grep -q 'styles\.css?v=' /usr/share/nginx/html/index.html
EXPOSE 80
