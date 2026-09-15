{ config, lib, pkgs, ... }:

let
  cfg = config.loom;
  embeddingsCfg = cfg.embeddings;
in
{
  options.loom.embeddings = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Enable LOOM notes embedding generation. Defaults to false until explicitly enabled.";
    };

    runtime = lib.mkOption {
      type = lib.types.enum [ "ollama" ];
      default = "ollama";
      description = "Embedding runtime used by LOOM notes embeddings.";
    };

    model = lib.mkOption {
      type = lib.types.str;
      default = "mxbai-embed-large";
      description = "Embedding model key passed to the local runtime.";
    };

    dimensions = lib.mkOption {
      type = lib.types.ints.positive;
      default = 1024;
      description = "Embedding vector dimensions for the configured model.";
    };

    quietWindowSeconds = lib.mkOption {
      type = lib.types.ints.positive;
      default = 600;
      description = "Seconds a changed note must remain stable before embedding work is queued.";
    };

    concurrency = lib.mkOption {
      type = lib.types.ints.positive;
      default = 1;
      description = "Global maximum concurrent embedding jobs for this node.";
    };

    ollama = {
      url = lib.mkOption {
        type = lib.types.str;
        default = "http://127.0.0.1:11434";
        description = "Local Ollama HTTP endpoint used for notes embeddings.";
      };

      installPackage = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = "Install the Ollama package without enabling the service.";
      };

      enableService = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = "Enable the NixOS Ollama service. This remains off by default.";
      };

      package = lib.mkOption {
        type = lib.types.package;
        default = pkgs.ollama;
        description = "Ollama package used when package installation or the service is enabled.";
      };
    };
  };

  config = lib.mkIf (cfg.enable && (embeddingsCfg.ollama.installPackage || embeddingsCfg.ollama.enableService)) {
    environment.systemPackages = [
      embeddingsCfg.ollama.package
    ];

    services.ollama = lib.mkIf embeddingsCfg.ollama.enableService {
      enable = true;
      package = embeddingsCfg.ollama.package;
    };
  };
}
