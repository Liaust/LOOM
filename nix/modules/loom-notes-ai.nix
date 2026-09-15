{ config, lib, pkgs, ... }:

let cfg = config.loom.notesAI;
in {
  options.loom.notesAI = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Install bounded local OCR dependencies for unified Notes pipelines.";
    };
    ocrLanguage = lib.mkOption {
      type = lib.types.str;
      default = "eng";
      description = "Tesseract language used by Notes PDF OCR.";
    };
    vision = {
      enable = lib.mkOption { type = lib.types.bool; default = false; description = "Enable local standalone-image descriptions."; };
      runtime = lib.mkOption { type = lib.types.enum [ "ollama" ]; default = "ollama"; };
      model = lib.mkOption { type = lib.types.str; default = ""; description = "Explicit local vision model; never downloaded automatically."; };
      ollamaURL = lib.mkOption { type = lib.types.str; default = "http://127.0.0.1:11434"; };
      maxBytes = lib.mkOption { type = lib.types.ints.positive; default = 20971520; };
      maxPixels = lib.mkOption { type = lib.types.ints.positive; default = 40000000; };
    };
  };

  config = lib.mkIf (config.loom.enable && cfg.enable) {
    assertions = [ { assertion = !cfg.vision.enable || cfg.vision.model != ""; message = "loom.notesAI.vision.enable requires an explicit local model."; } ];
    systemd.services.loomd.path = [ pkgs.poppler-utils pkgs.tesseract ];
  };
}
