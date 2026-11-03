#!/usr/bin/env bash
# smif.dev: static files in /opt/smif-web/site on the shared VPS, served by the nginx container `smif-web`
# on 127.0.0.1:8097, behind the shared Caddy (/opt/caddy/Caddyfile in the container diffy-caddy-1, block "smif.dev").
# DNS: smif.dev A -> the VPS (Hostinger DNS), www CNAME -> smif.dev.
set -euo pipefail
cd "$(dirname "$0")"
HOST=${DEPLOY_HOST:-root@46.202.143.240}

ssh "$HOST" "mkdir -p /opt/smif-web/site"
rsync -az --delete --exclude demo/ --exclude README.md --exclude LICENSE.md --exclude deploy.sh ./ "$HOST:/opt/smif-web/site/"
ssh "$HOST" 'docker inspect smif-web >/dev/null 2>&1 || docker run -d --name smif-web --restart unless-stopped -p 127.0.0.1:8097:80 -v /opt/smif-web/site:/usr/share/nginx/html:ro nginx:alpine >/dev/null'

# The Caddy block, once (backup first; validate before reload).
ssh "$HOST" 'bash -s' <<'REMOTE'
set -euo pipefail
F=/opt/caddy/Caddyfile
grep -q "^smif.dev {" "$F" && exit 0
B="$F.bak-smif-$(date +%Y%m%d-%H%M%S)"
cp "$F" "$B"
cat >> "$F" <<'CADDY'

smif.dev {
    encode zstd gzip
    header {
        X-Content-Type-Options nosniff
        Referrer-Policy strict-origin-when-cross-origin
        -Server
    }
    @static path /media/* /fonts/* /og.png /favicon.svg
    header @static Cache-Control "public, max-age=604800"
    reverse_proxy 127.0.0.1:8097
}

www.smif.dev {
    redir https://smif.dev{uri} permanent
}
CADDY
if ! docker exec diffy-caddy-1 caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null; then
  cp "$B" "$F"; echo "Caddyfile invalid: restored $B" >&2; exit 1
fi
docker exec diffy-caddy-1 caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile
REMOTE

curl -fsS -o /dev/null -w "smif.dev %{http_code}\n" https://smif.dev/ || echo "smif.dev not up yet (DNS or certificate still on the way)"
