#!/bin/sh
# ARCH = the fork's lowercase asset arch suffix (xray-linux-<ARCH>.zip)
# FNAME = the local rename target the panel image bundles in build/bin
case $1 in
    amd64)
        ARCH="amd64"
        FNAME="amd64"
        ;;
    i386)
        ARCH="386"
        FNAME="i386"
        ;;
    armv8 | arm64 | aarch64)
        ARCH="arm64"
        FNAME="arm64"
        ;;
    armv7 | arm | arm32)
        ARCH="armv7"
        FNAME="arm32"
        ;;
    armv6)
        ARCH="armv6"
        FNAME="armv6"
        ;;
    *)
        ARCH="amd64"
        FNAME="amd64"
        ;;
esac
mkdir -p build/bin
cd build/bin
# /releases/latest/download/<asset> redirects to the most recent published
# release on the fork, so no version pin is needed.
curl -sfLRO "https://github.com/sevaktigranyan305-netizen/Xray-core/releases/latest/download/xray-linux-${ARCH}.zip"
unzip "xray-linux-${ARCH}.zip"
rm -f "xray-linux-${ARCH}.zip" geoip.dat geosite.dat
mv xray "xray-linux-${FNAME}"
curl -sfLRO https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geoip.dat
curl -sfLRO https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat
curl -sfLRo geoip_IR.dat https://github.com/chocolate4u/Iran-v2ray-rules/releases/latest/download/geoip.dat
curl -sfLRo geosite_IR.dat https://github.com/chocolate4u/Iran-v2ray-rules/releases/latest/download/geosite.dat
curl -sfLRo geoip_RU.dat https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/geoip.dat
curl -sfLRo geosite_RU.dat https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/geosite.dat
cd ../../