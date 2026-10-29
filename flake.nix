{
  description = "wrtag development and disposable Linux testing environment";

  inputs.nixpkgs.url = "https://channels.nixos.org/nixos-unstable/nixexprs.tar.zst";

  outputs = { self, nixpkgs, ... }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
      environments = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          lib = pkgs.lib;
          runtime = [ pkgs.ffmpeg pkgs.flac pkgs.rsgain ];
          source = lib.cleanSourceWith {
            src = self;
            filter = path: type:
              let name = baseNameOf path;
              in !(builtins.elem name [ "testfiles" ".test-env" ".direnv" "PLAN.md" "TESTING.md" "contrib" "flake.nix" "flake.lock" ".envrc" ".gitignore" "nix" ]);
          };
          package = pkgs.buildGoModule.override { go = pkgs.go_1_27; } {
            pname = "wrtag";
            version = "0-unstable";
            src = source;
            vendorHash = "sha256-o2O3NaPGe9SMbVawzNv82QP71dSy5rNUxA1g6Clm87E=";
            subPackages = [ "cmd/wrtag" "cmd/wrtagweb" ];
            doCheck = false;
            nativeBuildInputs = [ pkgs.makeWrapper ];
            postInstall = ''
              for binary in wrtag wrtagweb; do
                wrapProgram "$out/bin/$binary" --prefix PATH : ${lib.makeBinPath runtime}
              done
            '';
          };
          unit = package.overrideAttrs {
            doCheck = true;
            nativeCheckInputs = runtime;
            checkPhase = ''
              runHook preCheck
              export GORACE=atexit_sleep_ms=0
              go test -race -count=1 -timeout 360s ./...
              runHook postCheck
            '';
          };
          testBinaries = package.overrideAttrs {
            pname = "wrtag-test-binaries";
            buildPhase = ''
              runHook preBuild
              mkdir -p $out/bin
              go test -c -o $out/bin/placement.test .
              go test -c -o $out/bin/cli.test ./cmd/wrtag
              go test -c -o $out/bin/web.test ./cmd/wrtagweb
              go build -o $out/bin/test2json cmd/test2json
              runHook postBuild
            '';
            installPhase = "true";
            postInstall = "";
          };
          node = { ... }: {
            system.stateVersion = "26.05";
            virtualisation = {
              memorySize = 4096;
              cores = 2;
              emptyDiskImages = [ 2048 2048 ];
            };
            boot.supportedFilesystems = [ "xfs" ];
            services.getty.autologinUser = "root";
            users.groups.music.gid = 2000;
            users.users = lib.genAttrs [ "producer" "importer" "observer" ] (name: {
              isNormalUser = true;
              extraGroups = [ "music" ];
            });
            environment.systemPackages = runtime ++ [ package testBinaries pkgs.xfsprogs pkgs.e2fsprogs pkgs.python3 ];
            systemd.services.test-filesystems = {
              wantedBy = [ "multi-user.target" ];
              before = [ "wrtagweb.service" ];
              serviceConfig = { Type = "oneshot"; RemainAfterExit = true; };
              path = [ pkgs.util-linux pkgs.e2fsprogs pkgs.xfsprogs pkgs.coreutils ];
              script = ''
                for spec in 'vdb ext4' 'vdc xfs'; do
                  set -- $spec
                  if ! blkid /dev/$1; then
                    if [ "$2" = xfs ]; then mkfs.xfs -m reflink=1 /dev/$1; else mkfs.ext4 -O casefold -E encoding=utf8 /dev/$1; fi
                  fi
                  mkdir -p /srv/$2
                  mount /dev/$1 /srv/$2
                  install -d -o importer -g music -m 2770 /srv/$2/library
                  install -d -o producer -g music -m 2770 /srv/$2/incoming
                done
              '';
            };
            systemd.services.fake-providers = {
              wantedBy = [ "multi-user.target" ];
              serviceConfig = {
                ExecStart = "${pkgs.python3}/bin/python3 ${./nix/fake-providers.py} ${source}/cmd/wrtag/testdata/responses";
                DynamicUser = true;
                NoNewPrivileges = true;
                PrivateTmp = true;
                IPAddressDeny = "any";
                IPAddressAllow = "localhost";
              };
            };
            systemd.services.wrtagweb = {
              wantedBy = [ "multi-user.target" ];
              requires = [ "test-filesystems.service" "fake-providers.service" ];
              after = [ "test-filesystems.service" "fake-providers.service" ];
              environment = {
                WRTAG_WEB_LISTEN_ADDR = "127.0.0.1:7373";
                WRTAG_WEB_AUTH = "basic-auth-from-api-key";
                WRTAG_FILE_MODE = "0660";
                WRTAG_WEB_DB_PATH = "/var/lib/wrtagweb/jobs.db";
                WRTAG_PATH_FORMAT = "/srv/xfs/library/{{ .Release.Title | safepath }}/{{ pad0 2 .Track.Position }} {{ .Track.Title | safepath }}{{ .Ext }}";
                WRTAG_MB_BASE_URL = "http://127.0.0.1:7380/musicbrainz/ws/2/";
                WRTAG_MB_RATE_LIMIT = "0";
                WRTAG_CAA_BASE_URL = "http://127.0.0.1:7380/coverartarchive/";
                WRTAG_CAA_RATE_LIMIT = "0";
              };
              serviceConfig = {
                ExecStart = "${package}/bin/wrtagweb";
                EnvironmentFile = pkgs.writeText "wrtagweb-test.env" "WRTAG_WEB_API_KEY=disposable-vm-only\n";
                User = "importer";
                SupplementaryGroups = [ "music" ];
                UMask = "0007";
                StateDirectory = "wrtagweb";
                StateDirectoryMode = "0700";
                CacheDirectory = "wrtagweb";
                CacheDirectoryMode = "0700";
                PrivateTmp = true;
                NoNewPrivileges = true;
                RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" ];
                IPAddressDeny = "any";
                IPAddressAllow = "localhost";
              };
            };
          };
          vmTest = pkgs.testers.runNixOSTest {
            name = "wrtag-filesystems";
            nodes.machine = node;
            globalTimeout = 1800;
            testScript = builtins.replaceStrings
              [ "@source@" "@testBinaries@" "@serviceTests@" "@audit@" ]
              [ "${source}" "${testBinaries}" "${./nix/service-tests.py}" "${./nix/audit-test-log.py}" ]
              (builtins.readFile ./nix/vm-test.py);
          };
          bootTest = pkgs.testers.runNixOSTest {
            name = "wrtag-vm-boot";
            nodes.machine = node;
            globalTimeout = 600;
            testScript = ''
              start_all()
              machine.wait_for_unit("multi-user.target")
              assert "kvm support: enabled" in machine.send_monitor_command("info kvm")
              machine.wait_for_unit("getty@tty1.service")
              machine.wait_for_open_port(7373)
              machine.wait_until_succeeds("ps -t tty1 -o user=,comm= | grep -E '^root +bash$'")
              machine.succeed("findmnt /srv/ext4; findmnt /srv/xfs; command -v wrtag wrtagweb ffmpeg flac rsgain")
              machine.succeed("touch /srv/xfs/library/boot-persistence")
              machine.shutdown()
              machine.start()
              machine.wait_for_unit("wrtagweb.service")
              machine.wait_for_open_port(7373)
              machine.succeed("test -f /srv/xfs/library/boot-persistence")
            '';
          };
        in {
          inherit package unit testBinaries node vmTest bootTest;
          shell = pkgs.mkShell {
            packages = runtime ++ (with pkgs; [ go_1_27 golangci-lint gopls delve gcc ]);
          };
        });
    in {
      devShells = forAllSystems (system: { default = environments.${system}.shell; });
      packages = forAllSystems (system: {
        default = environments.${system}.package;
        test-binaries = environments.${system}.testBinaries;
        vm = self.nixosConfigurations.${system}.config.system.build.vm;
      });
      checks = forAllSystems (system: {
        unit = environments.${system}.unit;
        vm = environments.${system}.vmTest;
        vm-boot = environments.${system}.bootTest;
      });
      nixosConfigurations = forAllSystems (system: nixpkgs.lib.nixosSystem {
        inherit system;
        modules = [
          environments.${system}.node
          "${nixpkgs}/nixos/modules/virtualisation/qemu-vm.nix"
          { networking.hostName = "wrtag-test"; }
        ];
      });
    };
}
