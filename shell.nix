{ pkgs ? import <nixpkgs> {} }:

pkgs.mkShell {
  name = "golang-dev";

  nativeBuildInputs = with pkgs; [
    go
    gotools
    pkg-config
    libayatana-appindicator
    gtk3
  ];
}
