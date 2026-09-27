# Hollowmere

[![CI](https://github.com/Destynyle/hollowmere/actions/workflows/ci.yml/badge.svg)](https://github.com/Destynyle/hollowmere/actions/workflows/ci.yml)

[English](README.md) · **Français**

Un monde de texte partagé, auquel on joue vraiment : une aventure textuelle
multijoueur (MUD) qui parle le protocole RFC 42TAP, avec un client navigateur
en WebSocket et le transport TCP brut conservé pour jouer depuis un terminal.

**▶ [Jouer à la démo dans le navigateur](https://destynyle.github.io/hollowmere/)**
— tout le serveur Go est compilé en WebAssembly et tourne dans la page : la
démo n'a besoin d'aucun serveur (ton monde est privé, ton personnage est
gardé dans le navigateur). Le vrai serveur multijoueur se lance avec
`docker compose up`.

C'est la version de production du projet de l'école 42 *The Answer
Protocol*, reconstruite pour tourner comme un vrai service. Le dépôt de
l'école reste tel qu'il a été rendu ; celui-ci est libéré de ses contraintes
(Go 1.27, bibliothèques externes, persistance, déploiement).

| Dialogues à choix | Combat au tour par tour |
|---|---|
| ![Une conversation à choix](docs/screenshots/dialogue.png) | ![Un combat contre le loup gris](docs/screenshots/combat.png) |

## Points forts

- **Un moteur, trois transports.** Le cœur du jeu ne connaît pas le réseau.
  WebSocket (navigateur), TCP brut (client terminal, netcat) et un pont dans
  la page (la démo WebAssembly) se branchent tous sur la même couche de
  session : même anti-flood, mêmes garanties d'ordre, même nettoyage.
- **Un monde décrit en données.** 50 salles réparties dans 7 fichiers de
  zone, 38 PNJ, 14 quêtes : dialogues à embranchements avec conditions et
  choix mémorisés, quêtes en plusieurs étapes, récompenses conditionnelles.
  Un validateur rejette les références cassées et signale le contenu
  inatteignable ; la CI garde le monde livré à zéro avertissement.
- **Un vrai RPG dessous.** Niveaux et expérience, statistiques à entraîner,
  trois emplacements d'équipement, compétences avec temps de recharge, et un
  donjon de groupe dont le boss s'adapte à la taille du groupe.
- **Jouer ensemble.** Groupes, messages privés, amis avec présence en ligne,
  classement (en jeu et sur `/top`), et échange d'objets à double
  confirmation exécuté en une seule opération : rien ne se perd ni ne se
  duplique.
- **Fait pour être hébergé.** Pas de comptes : une clé de reprise de 128 bits
  ramène le personnage. SQLite avec migrations versionnées, sauvegardes à
  chaud et restauration vérifiée, modération (rôles, mutes, bans sur clé et
  IP, journal d'audit), métriques Prometheus, tableau de bord Grafana, unité
  systemd durcie et profil de tunnel Cloudflare. Aucun secret n'arrive dans
  les logs.
- **Testé.** Tests unitaires et de bout en bout avec le détecteur de
  concurrence (`-race`), dont les sessions WebSocket, les migrations de
  sauvegarde et un échange scripté entre deux joueurs.

## Démarrage rapide

Prérequis : Go 1.27 (le Makefile trouve tout seul `~/sdk/go1.27.1`) ou
Docker.

```sh
make build
make run                 # client web sur http://127.0.0.1:8080, TCP sur 4243
make run-client NAME=moi # le client terminal, sur le même serveur
```

Avec Docker :

```sh
docker compose up -d --build
docker compose logs -f
```

La démo navigateur, en local :

```sh
make demo-serve          # construit site/ (Go -> WebAssembly), servi sur http://127.0.0.1:8000
```

## Organisation du code

| Paquet | Rôle |
|--------|------|
| `internal/proto` | Format RFC 42TAP : découpage en lignes, analyse des commandes, codes d'erreur |
| `internal/game` | Le monde et toutes les commandes ; aucun code réseau, parle aux joueurs via un `Sink` |
| `internal/session` | Session indépendante du transport : admission, anti-flood, file d'envoi bornée, nettoyage |
| `internal/tcpd` | Transport TCP brut (client terminal, netcat) |
| `internal/webd` | Client web, point d'entrée WebSocket, `/healthz`, `/metrics` |
| `internal/web` | Le client navigateur, embarqué dans le binaire |
| `internal/logs` | Logs JSON non bloquants au-dessus de `log/slog` |
| `internal/limit` | Seau à jetons partagé par l'anti-flood des sessions et les messages privés |
| `internal/store` | Persistance SQLite : personnages, classement, modération, migrations |
| `cmd/hollowmere`, `cmd/tapcli` | le serveur, et le client terminal |
| `cmd/demo`, `web-demo/` | le serveur compilé en WebAssembly, et le pont avec la page |
| `data/world/` | le monde, un fichier JSON par zone |

Un transport n'implémente que `session.Conn` (lire une ligne, écrire une
ligne, fermer). Le TCP et le WebSocket partagent donc la même détection des
abus, les mêmes garanties d'ordre et la même gestion des déconnexions.

Le navigateur parle en WebSocket : un message texte par ligne du protocole,
dans l'ordre. Plus de passerelle locale : le serveur sert le client et le
socket depuis la même origine.

## Configuration

Options en ligne de commande, ou variables d'environnement équivalentes
(pratique avec Docker) :

| Option | Variable | Défaut | Rôle |
|--------|----------|--------|------|
| `-http` | `TAP_HTTP` | `0.0.0.0:8080` | client web, WebSocket, santé et métriques |
| `-tcp` | `TAP_TCP` | `0.0.0.0:4243` | transport TCP brut (chaîne vide pour le désactiver) |
| `-world` | `TAP_WORLD` | `data/world` | dossier du monde (un fichier par zone), ou un fichier unique |
| `-log-file` | `TAP_LOG_FILE` | — | écrit aussi les logs JSON dans ce fichier |
| `-log-level` | `TAP_LOG_LEVEL` | `info` | `debug` journalise chaque commande et réponse |
| `-origins` | `TAP_ORIGINS` | même origine seulement | hôtes autorisés à ouvrir un WebSocket |
| `-trust-proxy` | `TAP_TRUST_PROXY=1` | désactivé | lit l'IP du client dans `CF-Connecting-IP` / `X-Forwarded-For` |
| `-metrics-token` | `TAP_METRICS_TOKEN` | — | exige ce jeton sur `/metrics` (en-tête Bearer ou `?token=`) |
| `-metrics-token-file` | `TAP_METRICS_TOKEN_FILE` | — | lit le jeton de `/metrics` dans un fichier (partagé avec Prometheus) |
| `-db` | `TAP_DB` | `state/hollowmere.db` | base des personnages ; `none` désactive la persistance |
| `-autosave` | — | `60s` | fréquence de sauvegarde des personnages connectés |
| `-backup <chemin>` | — | — | copie la base puis quitte (pour cron) |
| `-check` | — | — | valide le monde, affiche les avertissements puis quitte |
| `-grant NOM:RÔLE` | — | — | rend un personnage `moderator` ou `admin` (`none` retire), puis quitte |
| `-admins`, `-sanctions` | — | — | liste les rôles, ou les bans et mutes en cours, puis quitte |
| `-unban NOM` | — | — | lève les bans d'un personnage puis quitte |
| `-verify-db` | — | — | vérifie l'intégrité de la base puis quitte |

## Le monde

Le monde est dans `data/world/`, un fichier JSON par zone (`village`,
`crypt`, `forest`, `mines`, `marsh`, `hill`, `deep`), fusionnés au
chargement. Chaque fichier peut contenir des `rooms`, `items`, `npcs` et
`quests` ; sorties, apparitions et quêtes peuvent référencer librement les
identifiants des autres zones. Un identifiant n'est défini qu'une fois, et
l'en-tête (`name`, `start_room`, `safe_room`, `settings`) est dans
`_world.json`. `-world` accepte aussi un fichier unique contenant tout.

`make check-world` (inclus dans `make lint`) rejette les références cassées
et les salles inatteignables, et signale les erreurs probables : sorties à
sens unique, salles sans sortie, PNJ sans rôle ou jamais placés, objets que
personne ne peut obtenir, quêtes impossibles à finir, prérequis en boucle,
nœuds de dialogue inatteignables. Le monde livré n'a aucun avertissement, et
un test y veille.

**Arbres de dialogue.** Un PNJ garde sa simple liste `dialogue` (parcourue
par `TALK`) ou reçoit un `dialogue_tree` : des nœuds avec un `text` et des
`options`. Chaque option mène à un nœud `next` (`""` termine la
conversation), peut être cachée derrière une condition `if`, peut
mémoriser le choix avec `set_flag`, et peut porter une `quest` à accepter ou
à rendre. Un nœud sans option termine la conversation.

```json
"dialogue_tree": {
  "start": {
    "text": "Ah, a new face.",
    "options": [
      { "text": "You look worried.", "next": "worried", "if": { "quest": "q.moonpetal", "status": "none" } },
      { "text": "Goodbye.", "next": "" }
    ]
  },
  "worried": {
    "text": "The fever is back...",
    "options": [{ "text": "I'll help.", "next": "", "quest": "q.moonpetal" }]
  }
}
```

Une condition combine au choix : `quest` + `status` (`none`, `available`,
`active`, `ready`, `completed`, `not_completed`), `flag`, `no_flag`, `item`
(porté). Les options qui portent une quête ne s'affichent que tant que
cette quête peut être acceptée ou est en cours.

**Chaînes de quêtes.** Une quête simple a un seul objectif (`type`,
`target`, `count`). Une chaîne liste plutôt des `steps`, chacune avec une
`description`, un `type` (`fetch`, `deliver`, `kill`, `visit`, `talk`), une
`target` (objet, PNJ ou salle) et un `count`. Toutes les étapes sauf la
dernière se valident seules ; la dernière se rend au PNJ `turn_in`.
`requires` liste les quêtes prérequises, et `reward.bonus` accorde objets ou
points de vie en plus seulement si sa condition `if` est vraie à la fin —
par exemple un drapeau posé par un choix de dialogue.

## Progression

- **Expérience.** Vaincre un ennemi donne son `xp` (par défaut
  `pv/2 + 2×attaque + 2×défense`) à chaque joueur qui l'a blessé ; une quête
  donne son `reward.xp` (par défaut 50 par étape). Le niveau *n* demande
  `100 × n^1.5` points de plus. Un nouveau niveau apporte +10 PV max, +1
  attaque tous les deux niveaux, un soin complet et 2 points de statistiques
  (`EVT PLAYER LEVEL <n>`).
- **Statistiques**, augmentées avec `TRAIN <stat>` : force (+1 attaque),
  agilité (+1 vitesse, +2 % de coup critique), endurance (+5 PV max).
- **Équipement.** Trois emplacements : arme, armure, amulette. Seuls les
  objets portés comptent en combat. `EQUIP <objet>` / `UNEQUIP
  <emplacement>` ; un objet peut exiger un `min_level`. Un nouvel objet
  s'équipe tout seul si son emplacement est vide
  (`EVT PLAYER EQUIP <emplacement> <objet>`). Dans les fichiers du monde,
  `slot` est déduit de `attack` (arme) ou `defense` (armure) s'il n'est pas
  précisé.
- **Compétences**, listées par `SKILLS` et utilisées avec `SKILL <nom>` :
  `strike` (niveau 1, dégâts doublés, recharge 3), `parry` (niveau 2, bloque
  le coup suivant et riposte, recharge 4), `heal` (niveau 3, 20 + 5 PV par
  niveau, recharge 5). La recharge compte en tours de combat ; hors combat,
  un tour passe aussi toutes les 6 secondes.
- **Sauvegardes** en version 2. Un personnage en version 1 revient avec
  l'expérience des quêtes qu'il avait finies, et porte les meilleurs objets
  de son sac.

## Jouer ensemble

- **Messages privés** : `TELL <joueur> <message>` atteint un joueur connecté
  où qu'il soit, sous la forme `EVT PRIVATE MESSAGE <de> <message>`. En plus
  des limites de session, chaque joueur a droit à une rafale de 5 messages,
  puis un toutes les 2 s.
- **Amis** : `FRIEND ADD|REMOVE <nom>`, `FRIEND LIST`. La liste est
  sauvegardée avec le personnage, et les amis reçoivent
  `EVT FRIEND ONLINE|OFFLINE <nom>`.
- **Classement** : `TOP` en jeu, `/top` sur le web (page en lecture seule)
  et `/top.json`. Trié par niveau, expérience, quêtes finies, ennemis
  vaincus ; les joueurs connectés montrent leurs chiffres en direct.
- **Échange** : `TRADE <joueur>` fait la demande, la même commande de
  l'autre côté ouvre l'échange ; ensuite `TRADE OFFER|REMOVE <objet>`,
  `TRADE ACCEPT`, `TRADE CANCEL`, `TRADE INFO` (`TRADE WITH <joueur>` si un
  nom ressemble à une sous-commande). Les deux doivent accepter, toute
  modification retire les deux acceptations, et l'échange se fait en une
  seule opération sous le verrou du monde, après avoir vérifié que chaque
  objet est toujours en main. Changer de salle ou se déconnecter annule
  l'échange.
- **Donjon de groupe** : sous la Forge des Profondeurs, la Porte Ronde
  (`min_group: 2`) ne s'ouvre qu'à un groupe d'au moins deux membres,
  présents devant la porte ou déjà à l'intérieur (`ERR 419 GROUP_REQUIRED`).
  Ses ennemis utilisent `scale_per_player` : au début d'un combat, leurs PV
  augmentent de ce pourcentage pour chaque joueur supplémentaire dans la
  salle.

## Extensions du protocole

Hollowmere parle RFC 42TAP ; les clients qui ne connaissent que la RFC
continuent de fonctionner. Les ajouts :

| Ajout | Forme |
|-------|-------|
| Clé de reprise | `CONNECT <nom> [clé]`, `EVT PLAYER KEY <clé>` |
| Conversation | la réponse à `TALK <pnj>` peut porter `"options":[{"n":1,"text":"…"}]` ; `SAY <n>` en choisit une et répond sous la même forme, plus `"quest"` quand l'option a accepté ou rendu une quête. Sans options : la conversation est finie. |
| Étapes de quête | les réponses de quête portent `step`, `steps` et `objective` ; `EVT QUEST STEP <quête> <i>/<n>` quand une nouvelle étape commence |
| Progression | `EQUIP <objet>`, `UNEQUIP <emplacement>`, `TRAIN <stat>`, `SKILL <nom>`, `SKILLS` ; `STATUS` ajoute `level`, `xp`, `xp_next`, `stats`, `points`, `speed`, `critical_chance`, `equipment` ; `INSPECT` ajoute `slot`, `min_level`, `equipped` ; les réponses de quête ajoutent `xp` |
| Événements de progression | `EVT PLAYER XP <gagné> <xp>/<suivant>`, `EVT PLAYER LEVEL <n>`, `EVT PLAYER EQUIP <emplacement> <objet>` |
| Social | `TELL <joueur> <msg>`, `FRIEND ADD\|REMOVE\|LIST`, `TOP`, `TRADE <joueur>\|OFFER\|REMOVE\|ACCEPT\|CANCEL\|INFO\|WITH` |
| Événements sociaux | `EVT PRIVATE MESSAGE <de> <msg>`, `EVT FRIEND ONLINE\|OFFLINE <nom>`, `EVT TRADE REQUEST\|OPEN\|CANCEL <joueur>`, `EVT TRADE UPDATE\|DONE <json>` |
| Modération | `ANNOUNCE`, `KICK`, `MUTE`, `UNMUTE`, `BAN`, `UNBAN` ; événements `EVT SERVER ANNOUNCE <msg>`, `EVT SERVER KICK\|BAN <raison>`, `EVT SERVER MUTE <durée> <raison>` |
| Erreurs | `410 NOT_IN_CONVERSATION` (`SAY` sans conversation ouverte, ou le PNJ est parti), `411 INVALID_CHOICE`, `412 LEVEL_TOO_LOW`, `413 NOT_EQUIPPABLE`, `414 SKILL_NOT_READY`, `415 NO_STAT_POINTS`, `416 TOO_MANY_FRIENDS`, `417 NOT_TRADING`, `418 TRADE_BUSY`, `419 GROUP_REQUIRED`, `420 MUTED`, `403 FORBIDDEN`, `904 BANNED`, `404 SLOT_EMPTY`, `404 SKILL_NOT_FOUND` |

Changer de salle ferme la conversation. Dans le client terminal, tape le
numéro d'une réponse (ou `answer <n>`).

## Des personnages, sans comptes

Pas d'inscription, pas de mot de passe, pas d'e-mail. À la première visite,
le serveur crée un personnage et envoie sa **clé de reprise** — 128 bits
aléatoires — à ce seul client :

```text
C: CONNECT marin
S: OK connected
S: EVT PLAYER KEY jv4c2hq7t3m6k9x1b8n5r0wzye
```

Le client garde la clé (stockage local du navigateur, ou
`~/.config/hollowmere/keys.json` pour le client terminal) et la renvoie la
fois suivante :

```text
C: CONNECT marin jv4c2hq7t3m6k9x1b8n5r0wzye
S: OK connected          ← même salle, mêmes PV, même inventaire, mêmes quêtes
```

- La clé est le seul secret du personnage : qui la possède le joue. Le
  client web la garde masquée, avec **Copy** pour la noter ailleurs et
  **New** pour l'oublier et recommencer.
- Un nom appartient à la clé qui l'a créé : personne d'autre ne peut le
  prendre.
- Une clé inconnue n'est pas une erreur : elle démarre simplement un nouveau
  personnage.
- Les personnages sont sauvegardés à la déconnexion, toutes les 60 s et à
  l'arrêt du serveur.
- Les clients des autres groupes envoient `CONNECT <nom>` seul et reçoivent
  un nouveau personnage à chaque fois ; l'argument supplémentaire est une
  extension v2.

**Ce que deviennent les objets portés.** Les objets du monde (herbes,
potions, torches) retournent dans leur salle d'origine, pour que le monde
reste jouable pour les autres. Les récompenses de quête et le butin suivent
le personnage et sont recréés à son retour — ce qui libère aussi le butin de
l'ennemi qui l'avait lâché. Rien n'est dupliqué.

**Sauvegardes.** `deploy/backup.sh` lance `hollowmere -backup`, qui utilise
`VACUUM INTO` de SQLite : une copie cohérente pendant que les joueurs
continuent de jouer. Il compresse la copie et garde deux semaines
d'historique. La base vit dans `state/` (le volume Docker), avec les logs.

## Points d'accès

| Chemin | Rôle |
|--------|------|
| `/` | le client navigateur |
| `/ws` | un WebSocket par joueur, qui transporte les lignes du protocole |
| `/healthz` | JSON : état, joueurs, connexions, temps de fonctionnement |
| `/metrics` | texte Prometheus (jeton en `Authorization: Bearer` ou `?token=`) : joueurs, connexions, commandes, erreurs, compteurs d'abus |
| `/top`, `/top.json` | le classement, en lecture seule |

## Modération

Les modérateurs (`ANNOUNCE`, `KICK`, `MUTE`, `UNMUTE`) et les admins (en
plus `BAN`, `UNBAN`) sont nommés depuis la console avec `-grant`. Un ban
couvre la clé de reprise et l'adresse IP (la partie IP dure 7 jours au
plus) ; un mute bloque `CHAT` et `TELL` pendant une durée choisie. Chaque
action est journalisée et conservée dans la table `modlog`. Détails dans
`deploy/README.md` (en anglais).

## Exploitation

Mise en production (tunnel, sauvegardes, restauration, supervision, liste
de vérification) : **`deploy/README.md`** (en anglais).

- **Logs** : des lignes JSON sur la sortie standard (et dans
  `TAP_LOG_FILE`), écrites par une goroutine de fond qui abandonne des
  lignes plutôt que de ralentir le jeu.
  `tail -f state/server.log | jq -c 'select(.level != "INFO")'`
- **Limites** : 10 commandes/s par connexion (rafale de 40) puis exclusion,
  20 connexions par IP par 10 s, 16 simultanées par IP, 512 connexions au
  total, 1024 octets par ligne.
- **Secrets dans les logs** : les clés de reprise n'y apparaissent jamais,
  quel que soit le niveau.
- **Arrêt** : `SIGTERM` annonce le redémarrage aux joueurs
  (`EVT SERVER …`), vide les files d'envoi, puis quitte. Les clients
  navigateur se reconnectent d'eux-mêmes, avec un délai croissant.

## Feuille de route

1. ~~Socle : moteur porté, Docker, tests~~
2. ~~Transport : WebSocket + client web servi par le serveur, TCP conservé~~
3. ~~Persistance : SQLite, clé de reprise, sauvegarde périodique~~
4. ~~Contenu : monde agrandi, format étendu, dialogues à choix, chaînes de quêtes~~
5. ~~Progression : XP, niveaux, statistiques, équipement porté, compétences~~
6. ~~Social : messages privés, amis, classement, échange, donjons de groupe~~
7. ~~Production : modération, sauvegardes, tunnel Cloudflare, supervision~~

## Crédits

Réalisé par dsom et dlaktaf, à partir de leur projet 42. Protocole : RFC 42TAP.
