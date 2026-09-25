# Reprendre le projet sur un autre PC

Ce fichier sert à reprendre Hollowmere ailleurs : installation, état actuel,
décisions déjà prises, et la suite des étapes avec assez de détail pour s'y
remettre sans relire tout le code. La documentation du projet elle-même est
dans `README.md`, en anglais ; ce fichier-ci est une note interne.

---

## 1. Installer sur la nouvelle machine

Il faut **Go 1.27** (ou Docker seul) et `git`. Le projet n'a que deux
dépendances externes : `github.com/coder/websocket` et `modernc.org/sqlite`.

```sh
unzip hollowmere.zip -d ~/Documents && cd ~/Documents/tap-online

# Option A — Go local (recommandé pour développer)
curl -LO https://go.dev/dl/go1.27.1.linux-amd64.tar.gz
mkdir -p ~/sdk/go1.27.1 && tar xzf go1.27.1.linux-amd64.tar.gz -C ~/sdk/go1.27.1 --strip-components=1
rm go1.27.1.linux-amd64.tar.gz
make install        # le Makefile trouve ~/sdk/go1.27.1 tout seul
make lint test      # doit finir sans erreur
make run            # jeu sur http://127.0.0.1:8080, TCP sur 4243

# Option B — Docker uniquement
docker compose up -d --build
```

Vérifications rapides :

```sh
curl -s localhost:8080/healthz          # {"status":"ok",...}
printf 'CONNECT test\nLOOK\nQUIT\n' | nc localhost 4243
make run-client NAME=moi                # client terminal
```

Le dépôt git est inclus dans le zip : `git log` doit montrer les commits
existants. Configure ton identité si besoin :
`git config user.name` / `git config user.email`.

### Pièges déjà rencontrés

- **Quota disque** : sur les postes de l'école, `/home` est limité (~5 Go).
  Une compilation peut échouer avec « no space left on device » alors que
  `df` montre de la place. Libérer avec `go clean -cache`.
- **Port 4242 occupé** à l'école par un autre service : le projet utilise
  donc **4243** par défaut.
- **Captures d'écran de l'interface** sans interaction :
  `google-chrome --headless=new --no-sandbox --timeout=6000 --screenshot=x.png "http://127.0.0.1:8080/?name=test&autoconnect=1"`.
- **Logs** : `tail -f state/server.log | jq -c 'select(.level != "INFO")'`.
  Attention, `slog` met le nom de l'événement dans le champ `msg`.

---

## 2. Où en est le projet

Fait (étapes 1 à 3) :

- moteur repris du projet école, sur Go 1.27 avec `log/slog` ;
- couche `session` commune : un transport n'implémente que « lire une
  ligne, écrire une ligne, fermer » ; anti-flood, file d'envoi bornée et
  nettoyage sont partagés ;
- **WebSocket** (`/ws`) avec client web servi par le serveur, reconnexion
  automatique, plus **TCP brut** conservé pour le client CLI ;
- `/healthz` et `/metrics` (format Prometheus), en-têtes de sécurité,
  arrêt propre qui prévient les joueurs ;
- **persistance SQLite** avec clé de reprise, sauvegarde toutes les 60 s,
  sauvegarde à chaud (`-backup`, `deploy/backup.sh`) ;
- Docker, compose, Makefile, tests (moteur, WebSocket, stockage).

Reste à faire : étapes 4 à 7, détaillées plus bas.

### Décisions déjà prises (ne pas re-débattre)

| Sujet | Décision |
|-------|----------|
| Nom | Hollowmere (le village du monde) |
| Comptes | Aucun. Pseudo + clé de reprise de 128 bits, personnage en base |
| Transport | WebSocket pour les joueurs, TCP gardé pour le CLI et la compatibilité RFC |
| Base | SQLite, un fichier dans `state/` |
| Hébergement visé | Machine perso ou Raspberry Pi, derrière un tunnel Cloudflare |
| Compatibilité | Le protocole RFC 42TAP reste respecté ; nos ajouts sont des extensions documentées |

---

## 3. La suite des étapes

### Étape 4 — Contenu du monde

Objectif : passer de 11 salles à un vrai terrain de jeu (viser 40 à 60
salles), avec des dialogues qui ne soient pas une simple liste de répliques.

À faire :

1. **Découper les données** : remplacer `data/world.json` par
   `data/world/*.json` (une zone par fichier : village, forêt, mines,
   marais…), fusionnés au chargement. Adapter `LoadWorld` et garder la
   validation existante (sorties, références, atteignabilité).
2. **Dialogues à choix** : ajouter dans le format des PNJ un arbre
   `dialogue_tree` (nœud → texte + options menant à d'autres nœuds, avec
   conditions sur les quêtes). Nouvelles commandes :
   `TALK <pnj>` renvoie le nœud courant et ses options, `SAY <n>` choisit
   l'option `n`. Garder l'ancien format `dialogue: [...]` pour les PNJ
   simples, et la réponse RFC pour les clients étrangers.
3. **Chaînes de quêtes** : prérequis multiples existent déjà (`requires`) ;
   ajouter des quêtes d'étapes (`steps`) et des récompenses conditionnelles.
4. **Outillage** : `make check-world` doit valider toutes les zones ; y
   ajouter des avertissements (salle sans sortie retour, PNJ sans rôle,
   objet inatteignable, quête impossible à finir).
5. Écrire les tests de validation en même temps que le format.

Fichiers concernés : `internal/game/world.go`, `commands.go` (TALK, SAY),
`data/world/`, plus la doc du format dans `README.md`.

### Étape 5 — Progression du personnage

1. **XP et niveaux** : XP gagnée en tuant un ennemi (selon ses PV et son
   niveau) et en rendant une quête. Palier proposé :
   `xp_pour_niveau(n) = 100 × n^1.5`. Au niveau supérieur : `+10` PV max,
   `+1` attaque tous les 2 niveaux, soin complet, événement
   `EVT PLAYER LEVEL <n>`.
2. **Statistiques** : force, agilité, endurance, avec des points à
   répartir à chaque niveau (`TRAIN <stat>`). Les formules de combat de
   `combat.go` les utilisent à la place des constantes actuelles.
3. **Équipement porté** : aujourd'hui le meilleur objet transporté compte
   automatiquement. Ajouter des emplacements (arme, armure, amulette) et
   les commandes `EQUIP <objet>` / `UNEQUIP <emplacement>`, avec un niveau
   minimum par objet.
4. **Compétences** : 3 ou 4 suffisent au début (coup puissant, soin,
   parade), avec un temps de recharge en nombre de tours.
5. **Migration de sauvegarde** : `PlayerSave` passe en version 2. Écrire la
   migration dans `internal/game/persist.go` (une sauvegarde v1 se charge
   avec niveau 1 et statistiques de base) et un test qui charge une v1.

### Étape 6 — Social et multijoueur

1. **Messages privés** : `TELL <joueur> <message>` →
   `EVT PRIVATE MESSAGE <de> <message>`, avec refus si le joueur est hors
   ligne, et anti-spam réutilisant le seau à jetons.
2. **Amis** : `FRIEND ADD/REMOVE/LIST`, stockés en base ; notification
   quand un ami se connecte.
3. **Classement** : `store.Recent` existe déjà ; ajouter une table ou des
   colonnes (niveau, quêtes finies, ennemis vaincus) et une page web
   `/top` en lecture seule, plus la commande `TOP`.
4. **Commerce** : `TRADE <joueur>` avec double confirmation, pour ne pas
   perdre d'objet unique. C'est la partie la plus délicate : tout doit se
   faire sous le verrou du monde, en une seule opération.
5. **Donjon de groupe** : une zone dont l'entrée exige un groupe de 2 ou 3,
   avec un ennemi dont les PV dépendent du nombre de joueurs.

### Étape 7 — Mise en production

1. **Modération** : table `admins` (clé de reprise → rôle), commandes
   `KICK`, `MUTE <durée>`, `BAN`, `ANNOUNCE`, toutes journalisées. Le ban
   doit s'appliquer à la clé **et** à l'IP.
2. **Tunnel Cloudflare** (recommandé pour une machine perso : aucun port à
   ouvrir sur la box) :
   ```sh
   cloudflared tunnel login
   cloudflared tunnel create hollowmere
   # route: hollowmere.<ton-domaine> -> http://localhost:8080
   cloudflared tunnel route dns hollowmere hollowmere.<ton-domaine>
   cloudflared tunnel run hollowmere
   ```
   Puis lancer le serveur avec `TAP_TRUST_PROXY=1` et
   `TAP_ORIGINS=hollowmere.<ton-domaine>`. Le WebSocket passe dans le
   tunnel sans réglage particulier. Le TCP brut, lui, ne passe pas par un
   tunnel HTTP : ouvrir le port 4243 sur la box, ou le réserver au réseau
   local.
3. **Démarrage automatique** : `docker compose up -d` avec
   `restart: unless-stopped` (déjà configuré), ou une unité systemd si tu
   préfères sans Docker.
4. **Sauvegardes** : cron quotidien sur `deploy/backup.sh`, et une copie
   hors machine (rsync ou clé USB) — une sauvegarde sur le disque qui
   meurt ne sert à rien.
5. **Supervision** : `/metrics` est prêt. Le plus simple est Prometheus +
   Grafana dans le même `compose.yaml`, avec `TAP_METRICS_TOKEN` ; sinon
   une alerte basique sur `/healthz`.
6. **Avant d'ouvrir au public** : relire les limites d'usage
   (`session.DefaultConfig`), vérifier que les logs ne contiennent pas les
   clés de reprise, et tester une restauration de sauvegarde pour de vrai.

---

## 4. Conventions du dépôt

- `make lint test` doit passer avant chaque commit ; les tests tournent
  avec `-race`.
- Chaque nouvelle commande du protocole : gérée dans le moteur, exposée
  dans les deux clients, documentée dans `README.md` (section des
  extensions), et testée.
- Ne jamais insérer de texte venant du serveur avec `innerHTML` dans le
  client web : `textContent` uniquement.
- Aucun secret dans le dépôt : tout passe par des variables
  d'environnement (voir `compose.yaml`).

## 5. Reprendre avec Claude Code

Dans le dossier du projet :

```sh
cd ~/Documents/tap-online
claude
```

Puis, par exemple : « Lis REPRISE.md et continue à l'étape 4 : découper le
monde en zones et ajouter les dialogues à choix. »
