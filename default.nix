{ pkgs ? import
    (fetchTarball {
      name = "jpetrucciani-2026-04-28";
      url = "https://github.com/jpetrucciani/nix/archive/3130b3648ff46ff3a2202398ea497495eea86a7d.tar.gz";
      sha256 = "0mm1vnxr9ac4qjv2njfh7zzw3wl6j3f8f0lz8g18jlyd9c47qfpq";
    })
    { }
}:
let
  name = "cursor-admin-api-exporter";

  tools = with pkgs; {
    cli = [
      jfmt
      nixup
    ];
    go = [
      go
      go-tools
      gopls
    ];
    scripts = pkgs.lib.attrsets.attrValues scripts;
  };

  scripts = with pkgs; { };
  paths = pkgs.lib.flatten [ (builtins.attrValues tools) ];
  env = pkgs.buildEnv {
    inherit name paths; buildInputs = paths;
  };
in
(env.overrideAttrs (_: {
  inherit name;
  NIXUP = "0.0.10";
  CGO_ENABLED = "0";
})) // { inherit scripts; }
