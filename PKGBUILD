# Maintainer: Rasmus Steinke <rasi at xssn dot at>
pkgname=('subclerkd' 'subclerk-tui' 'subclerkc' 'subclerk-rofi' 'subclerk-musiclist')
pkgver=0.1.0
pkgrel=1
arch=('x86_64')
url="https://github.com/carnager/subclerk"
license=('MIT')
makedepends=('go')
source=("git+https://github.com/carnager/subclerk.git#tag=${pkgver}")
sha256sums=('SKIP')

build() {
  cd "$srcdir/subclerk"
  GOSUMDB=off GOMODCACHE="$srcdir/subclerk/.gomodcache" GOCACHE="$srcdir/subclerk/.gobuild" \
    ./build
}

package_subclerkd() {
  pkgdesc="Subclerk daemon for Navidrome/mpv"
  depends=('mpv')
  install -Dm755 "$srcdir/subclerk/bin/subclerkd" \
                  "$pkgdir/usr/bin/subclerkd"
  install -Dm644 "$srcdir/subclerk/subclerkd/subclerkd.service" \
                  "$pkgdir/usr/lib/systemd/user/subclerkd.service"
}

package_subclerk-tui() {
  pkgdesc="Terminal UI for Subclerk"
  depends=('subclerkd')
  install -Dm755 "$srcdir/subclerk/bin/subclerk-tui" \
                  "$pkgdir/usr/bin/subclerk-tui"
}

package_subclerkc() {
  pkgdesc="CLI client for Subclerk"
  optdepends=('subclerkd: local daemon')
  install -Dm755 "$srcdir/subclerk/bin/subclerkc" \
                  "$pkgdir/usr/bin/subclerkc"
}

package_subclerk-rofi() {
  pkgdesc="Rofi client for Subclerk"
  depends=('rofi')
  optdepends=('subclerkd: local daemon')
  install -Dm755 "$srcdir/subclerk/bin/subclerk-rofi" \
                  "$pkgdir/usr/bin/subclerk-rofi"
}

package_subclerk-musiclist() {
  pkgdesc="Static music list exporter for Subclerk"
  optdepends=('subclerkd: local daemon')
  install -Dm755 "$srcdir/subclerk/bin/subclerk-musiclist" \
                  "$pkgdir/usr/bin/subclerk-musiclist"
}
