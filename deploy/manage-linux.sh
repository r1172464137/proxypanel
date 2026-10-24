#!/usr/bin/env bash
# Linux/systemd staging and explicit gated activation helper.
set -euo pipefail
source "$(dirname "$(realpath "$0")")/upgrade-panel.sh"
usage() { echo "Usage: $0 install <panel|agent> <release-dir> <release-name> | preflight panel <release-name> [evidence.json] | activate panel <release-name> [evidence.json] | rollback-check panel <old-release-name> | uninstall <panel|agent> [--purge-agent-data]" >&2; exit 2; }
[[ $# -ge 2 ]] || usage
action=$1; component=$2
[[ $component == panel || $component == agent ]] || usage
[[ $(id -u) == 0 ]] || { echo 'Run as root on an explicitly authorized application/node host.' >&2; exit 1; }
base=/opt/proxypanel
marker=$base/.managed-by-proxypanel
service=proxypanel-$component
if [[ -e $base && ! -f $marker ]]; then echo 'Refusing unmanaged /opt/proxypanel directory.' >&2; exit 1; fi
case $action in
 preflight|activate|rollback-check)
  [[ $component == panel && $# -ge 3 && $# -le 4 ]] || usage
  [[ $3 =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ && -f $marker ]] || usage
  candidate=$base/releases/$3
  case $action in
   preflight) panel_preflight "$candidate" "${4:-${PANEL_UPGRADE_EVIDENCE_FILE:-}}" ;;
   activate) panel_activate "$base" "$3" "${4:-${PANEL_UPGRADE_EVIDENCE_FILE:-}}" ;;
   rollback-check) panel_rollback_check "$candidate" ;;
  esac
  ;;
 install)
  [[ $# == 4 ]] || usage
  src=$(realpath "$3"); name=$4
  [[ $name =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || usage
  [[ -f $src/$component && -f $src/SHA256SUMS && -f $src/deploy/$service.service && -f $src/deploy/manage-linux.sh && -f $src/deploy/upgrade-panel.sh ]] || { echo 'Incomplete release.' >&2; exit 1; }
  (cd "$src" && sha256sum --check --strict SHA256SUMS)
  [[ ! -e $base/releases/$name ]] || { echo 'Release name already exists; use a new immutable name.' >&2; exit 1; }
  if [[ -e $base/current && ! -L $base/current ]]; then echo 'current is not a managed symlink.' >&2; exit 1; fi
  if [[ -L $base/current ]]; then
   old=$(readlink -f "$base/current")
   [[ $old == "$base/releases/"* ]] || { echo 'Unmanaged current target.' >&2; exit 1; }
  fi
  install -d -m 0755 "$base/releases" /etc/proxypanel
  touch "$marker"
  cp -a -- "$src" "$base/releases/$name"
  chmod 0755 "$base/releases/$name/$component"
  account=proxypanel; [[ $component == panel ]] || account=proxypanel-agent
  if ! id "$account" >/dev/null 2>&1; then useradd --system --user-group --no-create-home --shell /usr/sbin/nologin "$account"; fi
  if [[ $component == panel ]]; then
   [[ -f /etc/proxypanel/panel.env ]] || { echo 'Create root-owned mode-0600 /etc/proxypanel/panel.env before starting.'; }
  else
   install -d -m 0700 -o "$account" -g "$account" /var/lib/proxypanel-agent
   [[ -f /etc/proxypanel/agent.json ]] || { echo 'Create root:proxypanel-agent mode-0640 /etc/proxypanel/agent.json before starting.'; }
  fi
  # Staging never changes current or an existing unit, even if service is stopped.
  if [[ ! -e /etc/systemd/system/$service.service ]]; then
   install -m 0644 "$src/deploy/$service.service" "/etc/systemd/system/$service.service"
   systemctl daemon-reload
  fi
  echo "Staged $name only; current and existing service unchanged. Panel: use preflight then activate; Agent: follow the coordinated maintenance plan."
  ;;
 uninstall)
  [[ $# == 2 || ( $# == 3 && $3 == --purge-agent-data && $component == agent ) ]] || usage
  [[ -f $marker ]] || { echo 'No managed installation.' >&2; exit 1; }
  systemctl disable --now "$service.service"
  rm -f -- "/etc/systemd/system/$service.service"
  systemctl daemon-reload
  if [[ ${3:-} == --purge-agent-data ]]; then
   [[ ! -L /var/lib/proxypanel-agent ]] || { echo 'Refusing symlink state directory.' >&2; exit 1; }
   echo 'Explicit purge selected: deleting Agent token, baselines and unsent usage queue.'
   rm -rf -- /var/lib/proxypanel-agent
  fi
  echo 'Service removed. Releases, /etc/proxypanel, databases, Redis and SDK/core binaries retained.'
  ;;
 *) usage ;;
esac
