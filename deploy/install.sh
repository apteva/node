#!/bin/sh
# Install apteva-node on a Raspberry Pi (Raspberry Pi OS Bookworm, 64-bit).
# Run on the Pi as root, next to the apteva-node binary built by build.sh:
#   sudo sh install.sh
set -eu

here=$(cd "$(dirname "$0")" && pwd)

id apteva-node >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin apteva-node
for g in gpio i2c video render input; do
  getent group "$g" >/dev/null && usermod -aG "$g" apteva-node
done

install -m 0755 "$here/apteva-node" /usr/local/bin/apteva-node
install -d -m 0755 /etc/apteva-node
if [ ! -f /etc/apteva-node/node.yaml ]; then
  # Without the example's dev server block: the device pairs from its own
  # setup screen. Keep server.url + token in node.yaml only to pre-provision.
  sed '/^server:/,/^ui:/{/^ui:/!d;}' "$here/node.example.yaml" > /etc/apteva-node/node.yaml
  chmod 0644 /etc/apteva-node/node.yaml
  echo "→ edit /etc/apteva-node/node.yaml to declare this device's hardware"
fi

# I2C and the camera are off by default on a fresh image.
command -v raspi-config >/dev/null && raspi-config nonint do_i2c 0 || true
command -v apt-get >/dev/null && apt-get install -y cog rpicam-apps-lite >/dev/null || true

# Lets the daemon join Wi-Fi from the setup screen.
[ -d /etc/polkit-1/rules.d ] && install -m 0644 "$here/50-apteva-node-network.rules" /etc/polkit-1/rules.d/

install -m 0644 "$here/apteva-node.service" /etc/systemd/system/
install -m 0644 "$here/apteva-node-screen.service" /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now apteva-node apteva-node-screen
echo "apteva-node $(/usr/local/bin/apteva-node --version) installed"
