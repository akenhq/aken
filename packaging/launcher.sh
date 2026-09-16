#!/bin/sh
# Aken launcher. install.sh installs this file as /usr/local/bin/aken and the
# collector as /usr/local/libexec/aken. Started as root, it switches to the
# unprivileged aken user, with that user's groups, a clean environment, no
# capabilities and no_new_privs, and then runs the collector. Started as any
# other user, it runs the collector as that user. The collector refuses root.
set -eu
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH
collector=/usr/local/libexec/aken
if [ "$(id -u)" -ne 0 ]; then
  exec "$collector" "$@"
fi
if ! getent passwd aken >/dev/null 2>&1; then
  printf '%s\n' 'aken: the aken user does not exist; run install.sh first' >&2
  exit 1
fi
exec /usr/bin/setpriv --reset-env --reuid=aken --regid=aken --init-groups \
  --inh-caps=-all --no-new-privs -- "$collector" "$@"
