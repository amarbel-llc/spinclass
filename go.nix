# go.nix — this module's dependencies (FDR 0008); go.mod, gomod2nix.toml and
# the package graph are rendered or derived from it inside nix. Edit through
# the escape hatch (godyn-go) or by hand.
{
  flakeInputs = {
    "code.linenisgreat.com/crap/go-crap/v2" = {
      input = "crap";
      subPath = "go-crap";
    };
    "code.linenisgreat.com/purse-first/libs/dewey" = {
      input = "purse-first";
      subPath = "libs/dewey";
    };
    "code.linenisgreat.com/ringmaster" = {
      input = "ringmaster";
    };
    "code.linenisgreat.com/tommy" = {
      input = "tommy";
    };
  };
  go = "1.26";
  module = "code.linenisgreat.com/spinclass";
  replace = { };
  require = {
    "code.linenisgreat.com/purse-first/libs/go-mcp" = {
      go = "1.26";
      hash = "sha256-JQDDIB3CFq0xICW4uMvIL9GosmIzMcu/tjGcVdgje8s=";
      version = "v0.6.2";
    };
    "code.linenisgreat.com/purse-first/libs/go-mcp/command/huh" = {
      go = "1.26";
      hash = "sha256-nZcZldhbxdD43F424KrFsfowPN3HTzoIqVmiAJovQjw=";
      version = "v0.0.5";
    };
    "code.linenisgreat.com/tap/go" = {
      go = "1.26";
      hash = "sha256-ifwq9+ER3gl317X0uizQkn2cYTtXiqWwPsFyfHZ91Bs=";
      version = "v0.2.0";
    };
    "github.com/atotto/clipboard" = {
      hash = "sha256-ZZ7U5X0gWOu8zcjZcWbcpzGOGdycwq0TjTFh/eZHjXk=";
      indirect = true;
      version = "v0.1.4";
    };
    "github.com/aymanbagabas/go-osc52/v2" = {
      go = "1.16";
      hash = "sha256-6Bp0jBZ6npvsYcKZGHHIUSVSTAMEyieweAX2YAKDjjg=";
      indirect = true;
      version = "v2.0.1";
    };
    "github.com/catppuccin/go" = {
      go = "1.19";
      hash = "sha256-otcMhI62ezoKGqzG7Owi/NROep7O0voJxp6bwXYg9+Q=";
      indirect = true;
      version = "v0.3.0";
    };
    "github.com/charmbracelet/bubbles" = {
      go = "1.24.2";
      hash = "sha256-Vz9QgctlzJqggPwfi48Lbn38ZJXu3Y71byp5uuuzUvU=";
      version = "v1.0.0";
    };
    "github.com/charmbracelet/bubbletea" = {
      go = "1.24.0";
      hash = "sha256-7wr85TLszu1CHNEMv+o4w+r24Z0xdzCgecPv+ZtRX/A=";
      version = "v1.3.10";
    };
    "github.com/charmbracelet/colorprofile" = {
      go = "1.24.2";
      hash = "sha256-d/NjM/ybG+bGRRRMMcjbPCFGFS5noZRMaL05Ix5r/II=";
      indirect = true;
      version = "v0.4.1";
    };
    "github.com/charmbracelet/harmonica" = {
      go = "1.16";
      hash = "sha256-fi5N0IXhSbbYHdSZFngCfpT4kdiEaKedqj8YpnlvX0o=";
      indirect = true;
      version = "v0.2.0";
    };
    "github.com/charmbracelet/huh" = {
      go = "1.23.0";
      hash = "sha256-vDqcsW9uBPDt0FaOA7Bij+Q9CkozggstOZ0r557TaC4=";
      version = "v1.0.0";
    };
    "github.com/charmbracelet/lipgloss" = {
      go = "1.18";
      hash = "sha256-RHsRT2EZ1nDOElxAK+6/DC9XAaGVjDTgPvRh3pyCfY4=";
      version = "v1.1.0";
    };
    "github.com/charmbracelet/log" = {
      go = "1.19";
      hash = "sha256-3w1PCM/c4JvVEh2d0sMfv4C77Xs1bPa1Ea84zdynC7I=";
      version = "v0.4.2";
    };
    "github.com/charmbracelet/x/ansi" = {
      go = "1.24.2";
      hash = "sha256-UToZIkqXl9MEppcRgbeBqaaMeAzRkGa0w3lVUs6sxWI=";
      indirect = true;
      version = "v0.11.6";
    };
    "github.com/charmbracelet/x/cellbuf" = {
      go = "1.24.2";
      hash = "sha256-0S60XaWhKZG+TB3Kqe1oMn2Okwdq53nym8XayVSHHiM=";
      indirect = true;
      version = "v0.0.15";
    };
    "github.com/charmbracelet/x/exp/strings" = {
      go = "1.19";
      hash = "sha256-NWe8LHXUtrrABWFhmAzLNYAZyJIwN3C/T2OdaInjl9E=";
      indirect = true;
      version = "v0.0.0-20240722160745-212f7b056ed0";
    };
    "github.com/charmbracelet/x/term" = {
      go = "1.24.0";
      hash = "sha256-KF7IU1Luxl/sZP6XjomWB2e3lxSUS4/5AahhapGir/4=";
      indirect = true;
      version = "v0.2.2";
    };
    "github.com/clipperhouse/displaywidth" = {
      go = "1.18";
      hash = "sha256-9CNyTZPSncKQ7Y0my9DR4WYXDjtDHYNL512D691WDAM=";
      indirect = true;
      version = "v0.9.0";
    };
    "github.com/clipperhouse/stringish" = {
      go = "1.18";
      hash = "sha256-Mp8M1CRbwr6dcJ4BD9tXD5I78ZgCFEm0GDxJv0GYReg=";
      indirect = true;
      version = "v0.1.1";
    };
    "github.com/clipperhouse/uax29/v2" = {
      go = "1.18";
      hash = "sha256-Men4JLhiuEtAx8ZSzId5ciRWhud68o3k/B48ppwyxkM=";
      indirect = true;
      version = "v2.5.0";
    };
    "github.com/dustin/go-humanize" = {
      go = "1.16";
      hash = "sha256-yuvxYYngpfVkUg9yAmG99IUVmADTQA0tMbBXe0Fq0Mc=";
      indirect = true;
      version = "v1.0.1";
    };
    "github.com/erikgeiser/coninput" = {
      go = "1.16";
      hash = "sha256-OWSqN1+IoL73rWXWdbbcahZu8n2al90Y3eT5Z0vgHvU=";
      indirect = true;
      version = "v0.0.0-20211004153227-1c3628e74d0f";
    };
    "github.com/go-logfmt/logfmt" = {
      go = "1.17";
      hash = "sha256-RtIG2qARd5sT10WQ7F3LR8YJhS8exs+KiuUiVf75bWg=";
      indirect = true;
      version = "v0.6.0";
    };
    "github.com/google/go-cmp" = {
      go = "1.21";
      hash = "sha256-JbxZFBFGCh/Rj5XZ1vG94V2x7c18L8XKB0N9ZD5F2rM=";
      indirect = true;
      version = "v0.7.0";
    };
    "github.com/google/shlex" = {
      go = "1.13";
      hash = "sha256-1f392pCmS7AXVKXIC1SvKlYtK/rvW47F5CCkGT2G6JM=";
      version = "v0.0.0-20191202100458-e7afc7fbc510";
    };
    "github.com/lucasb-eyer/go-colorful" = {
      go = "1.12";
      hash = "sha256-6BKrJsfmxie+YFAWzTYVPQfrwjQEXRo+J8LY+50C1BU=";
      indirect = true;
      version = "v1.3.0";
    };
    "github.com/mattn/go-isatty" = {
      go = "1.15";
      hash = "sha256-qhw9hWtU5wnyFyuMbKx+7RB8ckQaFQ8D+8GKPkN3HHQ=";
      version = "v0.0.20";
    };
    "github.com/mattn/go-localereader" = {
      hash = "sha256-JlWckeGaWG+bXK8l8WEdZqmSiTwCA8b1qbmBKa/Fj3E=";
      indirect = true;
      version = "v0.0.1";
    };
    "github.com/mattn/go-runewidth" = {
      go = "1.20";
      hash = "sha256-GpnbKplhX410Q/eIdknvWbYZgdav1keN+7wNUeOSMHE=";
      indirect = true;
      version = "v0.0.19";
    };
    "github.com/mitchellh/hashstructure/v2" = {
      go = "1.14";
      hash = "sha256-O4Yw4pPQECWe8DoVDIH2nUMN8Zl8waS7/O1sv18M2Xs=";
      indirect = true;
      version = "v2.0.2";
    };
    "github.com/muesli/ansi" = {
      go = "1.17";
      hash = "sha256-qRKn0Bh2yvP0QxeEMeZe11Vz0BPFIkVcleKsPeybKMs=";
      indirect = true;
      version = "v0.0.0-20230316100256-276c6243b2f6";
    };
    "github.com/muesli/cancelreader" = {
      go = "1.17";
      hash = "sha256-uEPpzwRJBJsQWBw6M71FDfgJuR7n55d/7IV8MO+rpwQ=";
      indirect = true;
      version = "v0.2.2";
    };
    "github.com/muesli/termenv" = {
      go = "1.17";
      hash = "sha256-hGo275DJlyLtcifSLpWnk8jardOksdeX9lH4lBeE3gI=";
      indirect = true;
      version = "v0.16.0";
    };
    "github.com/rivo/uniseg" = {
      go = "1.18";
      hash = "sha256-rDcdNYH6ZD8KouyyiZCUEy8JrjOQoAkxHBhugrfHjFo=";
      indirect = true;
      version = "v0.4.7";
    };
    "github.com/sahilm/fuzzy" = {
      hash = "sha256-f2VsDI6G+V2w31tSDzbZPi9EI2E7jRV6Aq8yeOorSZY=";
      indirect = true;
      version = "v0.1.1";
    };
    "github.com/xo/terminfo" = {
      go = "1.19";
      hash = "sha256-GyCDxxMQhXA3Pi/TsWXpA8cX5akEoZV7CFx4RO3rARU=";
      indirect = true;
      version = "v0.0.0-20220910002029-abceb7e1c41e";
    };
    "golang.org/x/exp" = {
      go = "1.25.0";
      hash = "sha256-JaDJGLIRoJjjvsg3dgfFuo7XApEJO2V4kUDmd58qTLI=";
      indirect = true;
      version = "v0.0.0-20260410095643-746e56fc9e2f";
    };
    "golang.org/x/sys" = {
      go = "1.25.0";
      hash = "sha256-aDQXqSTZES2l/132PBxhZN4ywldpPyfm7LByYCHzzwM=";
      indirect = true;
      version = "v0.43.0";
    };
    "golang.org/x/term" = {
      go = "1.25.0";
      hash = "sha256-FCiDvAfq7dgBGQuiDYDFJbj/JPawhrmPF2qdUEftQ1c=";
      version = "v0.42.0";
    };
    "golang.org/x/text" = {
      go = "1.25.0";
      hash = "sha256-/0t9C6Mc8kYjxweFB0us2lGKo8GovHhBiq5nlMOppC0=";
      indirect = true;
      version = "v0.36.0";
    };
    "golang.org/x/xerrors" = {
      go = "1.18";
      hash = "sha256-bE7CcrnAvryNvM26ieJGXqbAtuLwHaGcmtVMsVnksqo=";
      indirect = true;
      version = "v0.0.0-20240903120638-7835f813f4da";
    };
    "gopkg.in/yaml.v3" = {
      hash = "sha256-FqL9TKYJ0XkNwJFnq9j0VvJ5ZUU1RvH/52h/f5bkYAU=";
      indirect = true;
      version = "v3.0.1";
    };
    "mvdan.cc/sh/v3" = {
      go = "1.25.0";
      hash = "sha256-FM+2xpGELZLm4Gc09OE5Z3JeNLPMvzeb1wRcdAIeKy4=";
      indirect = true;
      version = "v3.13.1";
    };
  };
}
