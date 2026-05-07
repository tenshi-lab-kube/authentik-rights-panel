# Authentik Rights Panel

Petite page web en Go pour donner ou retirer rapidement l'acces SSO aux projets Authentik.

L'application fonctionne avec les groupes Authentik : un projet correspond a un groupe, et l'interface ajoute ou retire les personnes de ces groupes en un clic.

## Configuration

1. Copie `config.example.json` vers `config.json`.
2. Mets l'URL de ton Authentik dans `authentik_base_url`.
3. Ajoute tes projets dans `projects`, avec le nom exact du groupe Authentik dans `group`.
4. Cree un token API Authentik avec le droit de voir les utilisateurs/groupes et de modifier les groupes des utilisateurs.
5. Lance l'application avec le token en variable d'environnement :

```powershell
$env:AUTHENTIK_TOKEN="ton_token_api"
go run .
```

Ou avec Docker :

```powershell
Copy-Item docker-compose.example.yml docker-compose.yml
docker compose up -d --build
```

Par defaut la page ecoute seulement sur `127.0.0.1:8080`. Pour l'exposer au reseau, change `listen_addr`, idealement derriere ton reverse proxy avec SSO.

## Variables utiles

- `AUTHENTIK_TOKEN` : token API Authentik, obligatoire.
- `AUTHENTIK_BASE_URL` : remplace l'URL dans `config.json`.
- `LISTEN_ADDR` : remplace l'adresse d'ecoute, par exemple `0.0.0.0:8080`.
- `CONFIG_PATH` : chemin vers un autre fichier de config.

## Deploiement Kubernetes / Argo CD

Le dossier `k8s/` contient le deploiement Kubernetes. Le dossier `argocd/` contient une Application Argo CD qui pointe vers ce depot :

```text
https://github.com/tenshi-lab-kube/authentik-rights-panel.git
```

Avant d'activer l'application, ajuste `k8s/configmap.yaml` avec l'URL reelle d'Authentik et les groupes projet.

Le token API Authentik doit etre cree dans le cluster, hors Git :

```powershell
kubectl create namespace authentik-rights-panel
kubectl -n authentik-rights-panel create secret generic authentik-rights-panel-secret --from-literal=AUTHENTIK_TOKEN="ton_token_api"
```

Comme le depot est prive, cree aussi un secret de pull GHCR avec un token GitHub capable de lire les packages :

```powershell
kubectl -n authentik-rights-panel create secret docker-registry ghcr-auth --docker-server=ghcr.io --docker-username="ton_user_github" --docker-password="ton_token_github" --docker-email="ton_email"
```

L'image attendue est :

```text
ghcr.io/tenshi-lab-kube/authentik-rights-panel:latest
```

Service interne pour Cloudflare Tunnel :

```text
http://authentik-rights-panel.authentik-rights-panel.svc.cluster.local
```

## Securite

Ne mets jamais le token API dans Git. Le fichier `config.json` et les fichiers `.env` sont ignores par defaut.

Le mot de passe Proxmox que tu as donne n'est pas necessaire pour ce panneau : il vaut mieux piloter les droits via l'API Authentik et garder Proxmox separe.
