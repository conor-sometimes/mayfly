# The shipped distro catalog: every distro the matrix knows how to boot, as one
# attrset. A consumer does not define a distro; it flips one on:
#
#   distros = { ubuntu-2404.enable = true; fedora-44.enable = true; };
#
# Each entry is a base family (the package manager, the family tag, and the
# generic-name -> package map) overlaid with one version's image and checksum.
# Adding a version of a distro already here is one URL and one hash:
#
#   ubuntu-2210 = mkVersion ubuntu { title = "Ubuntu 22.10"; image = { url = "..."; sha256 = "..."; }; };
#
# A distro the catalog lacks is still addable as one extra attr in the
# consumer's own `distros`, with the same fields (see README).
{ lib }:
let
  # mkVersion overlays a version's own fields on its base family, deep-merging
  # the packages map so a version can override one spelling (Alma's auditctl)
  # without restating the rest.
  mkVersion = base: ver:
    base // ver // { packages = (base.packages or { }) // (ver.packages or { }); };

  # --- base families: the per-distro knowledge shared across versions ---

  # The commands the banken-family matrix and the generic engine resolve. A
  # consumer that names a package not here (subject.packages) gets a clear
  # error; extend the base or the version.
  rhelPackages = {
    auditd = "audit";
    auditctl = "audit";
    sudo = "sudo";
    curl = "curl";
    bash = "bash";
    python3 = "python3";
    runuser = "util-linux";
    pkill = "procps-ng";
    tar = "tar";
    nmap = "nmap";
    tcpdump = "tcpdump";
  };

  rhel = {
    pm = "dnf";
    family = "rhel";
    packages = rhelPackages;
  };

  debian = {
    pm = "apt";
    family = "debian";
    packages = {
      auditd = "auditd";
      auditctl = "auditd";
      sudo = "sudo";
      curl = "curl";
      bash = "bash";
      python3 = "python3";
      runuser = "util-linux";
      pkill = "procps";
      tar = "tar";
      nmap = "nmap";
      tcpdump = "tcpdump";
    };
  };

  # Ubuntu shares Debian's apt knowledge; it is its own base so its many
  # versions sit under one place.
  ubuntu = debian // { family = "debian"; };

  suse = {
    pm = "zypper";
    family = "suse";
    packages = rhelPackages // { pkill = "procps"; };
  };

  arch = {
    pm = "pacman";
    family = "arch";
    packages = rhelPackages // { python3 = "python"; };
  };
in
{
  inherit mkVersion ubuntu debian rhel suse arch;

  catalog = {
    # Ubuntu: the proven walking skeleton, plus a second version to show the
    # base/overlay multi-version pattern (add a version = one URL + one hash).
    ubuntu-2404 = mkVersion ubuntu {
      title = "Ubuntu 24.04";
      image = {
        url = "https://cloud-images.ubuntu.com/releases/noble/release-20261017/ubuntu-24.04-server-cloudimg-amd64.img";
        sha256 = "04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94";
      };
    };
    ubuntu-2204 = mkVersion ubuntu {
      title = "Ubuntu 22.04";
      image = {
        url = "https://cloud-images.ubuntu.com/releases/jammy/release-20261020/ubuntu-22.04-server-cloudimg-amd64.img";
        sha256 = "2099e63da0b822ef8e05e3d596c1b402eaaae658fc08ab919cb8495bfcdc488b";
      };
    };

    debian-13 = mkVersion debian {
      title = "Debian 13";
      image = {
        url = "https://cloud.debian.org/images/cloud/trixie/20261026-2643/debian-13-genericcloud-amd64-20261026-2643.qcow2";
        sha512 = "3ace18e33bdd036edb49d1f79342bd01d2ab980e35194edb0b410435cd543538d286a0bd13bbb0835f7862306e3d385ce2f6a70d8ecf0bd961111166adbc1b8e";
      };
    };

    rocky-9 = mkVersion rhel {
      title = "Rocky 9";
      image = {
        url = "https://dl.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud-Base-9.8-20260525.0.x86_64.qcow2";
        sha256 = "92c206cc6f790c61583247eefe87890f8828420662c17cacf247cec78ab4eec8";
      };
    };

    alma-10 = mkVersion rhel {
      title = "AlmaLinux 10";
      image = {
        url = "https://repo.almalinux.org/almalinux/10/cloud/x86_64/images/AlmaLinux-10-GenericCloud-10.2-20261012.0.x86_64.qcow2";
        sha256 = "cbee4726fc0f5a60694bb4f289003cc221e715dfbacebcce76b54daf386d8eca";
      };
      packages = { auditctl = "audit-rules"; };
    };

    fedora-44 = mkVersion rhel {
      title = "Fedora 44";
      image = {
        url = "https://dl.fedoraproject.org/pub/fedora/linux/releases/44/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-44-1.7.x86_64.qcow2";
        sha256 = "28680fe5b371a5a82ebf43a31926e086a168e59949d03969c5093e7071f90b7f";
      };
      family = "rhel";
      packages = { auditctl = "audit-rules"; };
    };

    amazonlinux-2023 = mkVersion rhel {
      title = "Amazon Linux 2023";
      image = {
        url = "https://cdn.amazonlinux.com/al2023/os-images/2023.12.20261102.0/kvm/al2023-kvm-2023.12.20261102.0-kernel-6.1-x86_64.xfs.gpt.qcow2";
        sha256 = "0b7457a24a9467ad0e923822c14f2ab706bbc5cbd68544e00af0d8247f225b4a";
      };
      packages = { curl = "curl-minimal"; };
    };

    opensuse-leap-16 = mkVersion suse {
      title = "openSUSE Leap 16";
      image = {
        url = "https://downloadcontent.opensuse.org/distribution/leap/16.0/appliances/Leap-16.0-Minimal-VM.x86_64-Cloud-Build18.89.qcow2";
        sha256 = "2f328a0135f9c8bf62c5ec012405169f76188ea7abf18f9c65cb0ea5374c042b";
      };
    };

    arch = mkVersion arch {
      title = "Arch Linux";
      image = {
        url = "https://geo.mirror.pkgbuild.com/images/v20261015.611680/Arch-Linux-x86_64-cloudimg-20261015.611680.qcow2";
        sha256 = "08754e28d91b1680086c6211ef4df13559e766fff7cdb4006489b14b5d281f08";
      };
    };

    # NixOS as the ninth attribute is roadmap: its image is a derivation
    # (a cloud-init qcow2 from config.system.build), not a vendor URL. The
    # engine accepts an image built by a function (image.drv), but building the
    # NixOS cloud image is a day of its own (open question 6 in the design), so
    # the entry is documented here and left for that work.
    # nixos = mkVersion nixosBase { title = "NixOS 26.05"; image.drv = pkgs: <cloud qcow2>; };
  };
}
