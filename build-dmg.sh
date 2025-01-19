#!/bin/bash

APP_NAME="WalkingPad Controller"
BUNDLE_ID="dev.timoster.walkingpad-controller"
APP_DIR="dist/$APP_NAME.app"
DMG_NAME="$APP_NAME.dmg"
OUTPUT_DIR="dist"
STAGING_DIR="$OUTPUT_DIR/dmg_staging"

# Ensure output directory exists
mkdir -p "$OUTPUT_DIR"

# Create the .app bundle structure
mkdir -p "$APP_DIR/Contents/MacOS"
mkdir -p "$APP_DIR/Contents/Resources"
echo "APPL????" > "$APP_DIR/Contents/PkgInfo"

# Copy your binary into the app bundle
go build -o "$APP_DIR/Contents/MacOS/$APP_NAME"

# Add an Info.plist file
cat > "$APP_DIR/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>$APP_NAME</string>
    <key>CFBundleIdentifier</key>
    <string>$BUNDLE_ID</string>
    <key>CFBundleName</key>
    <string>$APP_NAME</string>
    <key>CFBundleVersion</key>
    <string>1.0</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>LSUIElement</key>
    <true/>
    <key>NSBluetoothAlwaysUsageDescription</key>
    <string>Used to connect to Walking Pads.</string>
    <key>NSBluetoothPeripheralUsageDescription</key>
    <string>Used to connect to Walking Pads.</string>
</dict>
</plist>
EOF

# Prepare the DMG staging directory
mkdir -p "$STAGING_DIR"
cp -R "$APP_DIR" "$STAGING_DIR"

# Create a symbolic link to the Applications folder
ln -s /Applications "$STAGING_DIR/Applications"

# Create the DMG file
hdiutil create -volname "$APP_NAME Installer" \
    -srcfolder "$STAGING_DIR" \
    -ov -format UDZO "$OUTPUT_DIR/$DMG_NAME"

# Cleanup
rm -rf "$STAGING_DIR"
rm -rf "$APP_DIR"

echo "Installer DMG created at $OUTPUT_DIR/$DMG_NAME"
