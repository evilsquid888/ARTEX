# syntax=docker/dockerfile:1
#
# Runtime image (no compilation inside the image): install common tools only and ship
# a **pre-built Linux single binary**. The binary is cross-compiled by the CI binaries
# job (pure Go, no QEMU) and placed at dist/<TARGETARCH>/artex in the build context.
# This way multi-arch builds only need to emulate the apt layer for arm64 instead of
# emulating the Next/Go compilation, which is much faster.
#
# To build the image manually, prepare the binary yourself first:
#   cd web && npm run build:static && cd ..
#   cp -r web/out server/webui/dist
#   CGO_ENABLED=0 GOARCH=amd64 go build -tags embedui -o dist/amd64/artex ./cmd/artex
#   docker build -t artex:local .
FROM python:3.12-slim-bookworm
ARG TARGETARCH
# Common tools: ripgrep / curl / vim, plus a set of recon staples (add/remove as needed).
# Node 20.x comes from NodeSource: bookworm's apt nodejs is 18, but Playwright requires >=20.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
# Web-recon toolbox (drost-community take 1, #100): pinned release binaries installed
# into /usr/local/bin so hunt workers can run them directly through their Bash tool.
# Versions pinned per tool; upgrade deliberately. sqlmap is a python checkout (no
# release binaries) symlinked into PATH.
ARG NUCLEI_VERSION=v3.11.1
ARG HTTPX_VERSION=v1.12.0
ARG SUBFINDER_VERSION=v2.17.0
ARG KATANA_VERSION=v1.8.0
ARG FFUF_VERSION=v2.3.0
ARG DALFOX_VERSION=v3.2.4
RUN set -eux; \
    base="https://github.com"; \
    pd_fetch() { repo="$1"; ver="$2"; name="$3"; nov="${ver#v}"; \
      curl -fsSL "$base/$repo/releases/download/$ver/${name}_${nov}_linux_${TARGETARCH}.zip" -o /tmp/t.zip \
      && unzip -o -q /tmp/t.zip -d /tmp/td \
      && find /tmp/td -type f -name "$name" -exec install -m 0755 {} /usr/local/bin/ \; \
      && rm -rf /tmp/t.zip /tmp/td; }; \
    case "$TARGETARCH" in amd64) FFUF_ARCH=amd64; DALFOX_ARCH=x86_64;; arm64) FFUF_ARCH=arm64; DALFOX_ARCH=aarch64;; esac; \
    pd_fetch projectdiscovery/nuclei "$NUCLEI_VERSION" nuclei; \
    pd_fetch projectdiscovery/httpx "$HTTPX_VERSION" httpx; \
    pd_fetch projectdiscovery/subfinder "$SUBFINDER_VERSION" subfinder; \
    pd_fetch projectdiscovery/katana "$KATANA_VERSION" katana; \
    tar_fetch() { url="$1"; name="$2"; \
      curl -fsSL "$url" -o /tmp/t.tgz \
      && mkdir -p /tmp/td && tar -xzf /tmp/t.tgz -C /tmp/td \
      && find /tmp/td -type f -name "$name" -exec install -m 0755 {} /usr/local/bin/ \; \
      && rm -rf /tmp/t.tgz /tmp/td; }; \
    tar_fetch "$base/ffuf/ffuf/releases/download/$FFUF_VERSION/ffuf_${FFUF_VERSION#v}_linux_${FFUF_ARCH}.tar.gz" ffuf; \
    tar_fetch "$base/hahwul/dalfox/releases/download/$DALFOX_VERSION/dalfox-${DALFOX_VERSION}-linux-${DALFOX_ARCH}-musl.tar.gz" dalfox; \
    git clone --depth 1 https://github.com/sqlmapproject/sqlmap /opt/sqlmap; \
    ln -s /opt/sqlmap/sqlmap.py /usr/local/bin/sqlmap; \
    nuclei -version >/dev/null; httpx -version; subfinder -version; katana -version; \
    ffuf -V >/dev/null; dalfox --version >/dev/null 2>&1 || true; sqlmap --version >/dev/null

# bbot recursive recon orchestrator (#117): blacklanternsecurity/bbot, AGPL-3.0
# (same license as ARTEX; standalone CLI dependency, not linked code). Pinned
# version, own venv to isolate from the image python. Module dependencies are
# preinstalled (--install-all-deps) so scans never need pip at runtime, which
# would fail under a program's egress allowlist.
ARG BBOT_VERSION=3.0.2
RUN set -eux; \
    python -m venv /opt/bbot; \
    /opt/bbot/bin/pip install --no-cache-dir "bbot==${BBOT_VERSION}"; \
    ln -s /opt/bbot/bin/bbot /usr/local/bin/bbot; \
    bbot -y --install-all-deps; \
    bbot --version >/dev/null; \
    # arm64 host patch: gowitness ships an x86_64 Chrome binary and hard-fails
    # setup on this arch, which would mask the sanity-check "ERRR" grep below.
    # Chromium is installed later (via Playwright) and bbot can pick it up at
    # runtime via chrome_path if gowitness is explicitly requested. Drop it
    # from the sanity-check module list only; the gowitness module itself
    # stays installed in the image.
    bbot --dry-run -t example.com -m wafw00f,paramminer_getparams,lightfuzz,sslcert >/dev/null 2>&1 || true; \
    bbot --dry-run -t example.com -m wafw00f,paramminer_getparams,lightfuzz,sslcert --no-color >/tmp/bbot-dry.log 2>&1 || true; \
    ! grep -qE "Installing|ERRR|CRITICAL" /tmp/bbot-dry.log || { cat /tmp/bbot-dry.log; exit 1; }


# Preinstall Playwright MCP and CLI (globally) so no npx network download is needed at runtime.
# @playwright/mcp: browser MCP runs directly via `npx @playwright/mcp` (installed globally, no -y/@latest needed).
# @playwright/cli: provides playwright-cli, verified with --help right after install.
# Then install playwright (browser management) and preinstall chromium plus its system
# dependencies via --with-deps, so MCP/CLI work on first start inside the container
# without downloading browsers over the network.
RUN npm install -g @playwright/mcp@latest @playwright/cli@latest playwright@latest \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/*

# Active Directory / Windows attack toolbox. HTB AD chains (ADCS ESC7/ESC8, NTLM
# relay, Kerberos, SMB/LDAP/RDP, WinRM) need these; the recon staples above don't
# cover them. Baked in so they survive image rebuilds (they were hand-installed into
# a running container during the Omniscient push). impacket/certipy/bloodhound go
# into the image's system python (CLI scripts land in /usr/local/bin); evil-winrm is
# a ruby gem. Pinned; upgrade deliberately. NOTE: deliberately no SageMath/conda here
# — that was a one-off for a single isogeny crypto challenge, not general tooling.
#
# Also rewrite the apt sources to https: the agent runs under a 443-only egress
# allowlist at runtime, so http (port 80) apt fetches are dropped. Harmless at build.
ARG IMPACKET_VERSION=0.13.1
ARG CERTIPY_VERSION=5.1.0
ARG BLOODHOUND_PY_VERSION=1.9.0
ARG EVILWINRM_VERSION=4.1
RUN set -eux; \
    export DEBIAN_FRONTEND=noninteractive; \
    sed -i -E 's|http://(deb\|security)\.debian\.org|https://\1.debian.org|g' \
      /etc/apt/sources.list.d/debian.sources 2>/dev/null || true; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
      smbclient krb5-user ldap-utils freerdp2-x11 \
      ruby ruby-dev build-essential libreadline-dev; \
    pip install --no-cache-dir \
      "impacket==${IMPACKET_VERSION}" \
      "certipy-ad==${CERTIPY_VERSION}" \
      "bloodhound==${BLOODHOUND_PY_VERSION}"; \
    gem install evil-winrm -v "${EVILWINRM_VERSION}" --no-document; \
    # sanity checks (fail the build if any core tool is missing)
    secretsdump.py -h >/dev/null; \
    ntlmrelayx.py -h >/dev/null; \
    certipy -h >/dev/null; \
    bloodhound-python -h >/dev/null; \
    evil-winrm --help >/dev/null 2>&1 || evil-winrm 2>/dev/null || true; \
    command -v xfreerdp >/dev/null; \
    command -v smbclient >/dev/null; \
    command -v ldapsearch >/dev/null; \
    command -v kinit >/dev/null; \
    rm -rf /var/lib/apt/lists/*
WORKDIR /app
# pre-built binary for the matching architecture (dist/amd64/artex or dist/arm64/artex)
COPY dist/${TARGETARCH}/artex /app/artex
# Daemon startup script: relaunches the process after a crash and forwards SIGTERM
# to artex — docker stop only signals PID 1; without forwarding, artex never receives it,
# cannot shut down gracefully, and is hard-killed by SIGKILL after 10 seconds.
COPY start.sh /app/start.sh
RUN chmod +x /app/artex /app/start.sh
COPY skills/ /app/skills/
# persistence point for data/ (SQLite + jwt.key)
VOLUME ["/app/data"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]
