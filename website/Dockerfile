# The validated static artifact is the only application content in this image.
FROM caddy:2.10.2-alpine@sha256:4c6e91c6ed0e2fa03efd5b44747b625fec79bc9cd06ac5235a779726618e530d
COPY Caddyfile /etc/caddy/Caddyfile
COPY public/ /srv/
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD wget -q --spider http://127.0.0.1:8080/ || exit 1
