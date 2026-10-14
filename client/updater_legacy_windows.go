//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
)

// These published releases share the same authenticated readiness protocol,
// updater-owned backup names, and default Wails browser options. Each digest
// is pinned to its signed release manifest; see testdata/legacy-update-releases.
// Never select this mode from a version string, environment flag, or a hash
// supplied by the update server. Unknown executables keep the normal policy.
func legacyUpdateRelease(hash [sha256.Size]byte) string {
	switch hex.EncodeToString(hash[:]) {
	case "141444ace48e2506c1f5570dc90ef746f8e84b0102dc50bc5627580de6419d12":
		return "v0.5.24"
	case "d941be207e0e11b9871dfa637afb307e97b1c0278e0e9aafd66ea32d8bd7ba60":
		return "v0.5.25"
	case "941ecb37c1d0a2d04485660041fdd20144b40ebb8d23d8a4754741b94efde2c3":
		return "v0.5.26"
	case "db1efaab82c8b3eb90094a03c7173ec2650ca4285af23c4e1e68bc22c205d0d2":
		return "v0.5.27"
	case "cd8f753c757c717e58e107a6d2ad3d1865e12b67cb003911c9551266cabc609d":
		return "v0.5.28"
	case "560a4f81dd8a52981af91d2286892935006244040638a29e94eb8d542b9d3c17":
		return "v0.5.29"
	case "c3c2bfc0cc4bb49f88776481f9ab0d8c90fad45565c319251d70ead453636952":
		return "v0.5.30"
	case "f67d8de8e31f380e24bff4292fb29f9a7e4ed516a3af3c1dbd86618c3285ec8b":
		return "v0.5.31"
	case "1401516acb6e79b1a1888f4e7f23a8622a8bac6f13e3d2fe925a43479fc858b9":
		return "v0.5.32"
	case "0d2abd2cda092e0b51d2a0cfc2cdaf065e4813facb5d88c65001aa92cae89219":
		return "v0.5.33"
	case "731365b0b912997d4cccf285d1c3d3c9b59a695363b485539ab16930affc546b":
		return "v0.5.34"
	case "71cd821139efae87f9aaa0bfb32951895a676e0652505c614686c281943b68b5":
		return "v0.5.35"
	case "873139721f829ffa82047fb27df78e0490e4f5d454d620c5ca636e04b09cae4c":
		return "v0.5.36"
	case "bae390db4653ca83f225bfbebbbccdc3c9a5b9588a2e0f255335d5638444cc7c":
		return "v0.5.37"
	case "47401fb444091f660b84c772f2f111525017a1477a825f297c96fff64d5c67a3":
		return "v0.5.38"
	case "779f2af7a583efa82160194b20c990847921bbbe31c92591f623fc8684d3c06c":
		return "v0.5.39"
	case "34891d995a7b14ddf65c4333c877e8499490845fa065e8ca3c6952387cd441eb":
		return "v0.5.40"
	case "bb300a0e21cc4cda696739d110509291237a55cbcfc1abccc80d3ed46da514a0":
		return "v0.5.41"
	case "5cdfe1f64aa2a71792cb27d1c614cf7e23d039819a481b939365f47c3c6b0a18":
		return "v0.5.42"
	case "1ad71c5f005e672176c1d3805df691e03c08a23a6acb4c3985aea1af3bdf2699":
		return "v0.5.43"
	case "04bc3fd2186ab90ce0df157a282cae5bdf1c3789ea08b02163a7957177d44d40":
		return "v0.5.44"
	case "9f32df273cdf17abdb7fa73b58241cc16ed174f6499ac0ec7094822eb995c99c":
		return "v0.5.45"
	case "fdff89f23185a928a590a218a4d70e86fd5099748a400278d56b7bec99e6a688":
		return "v0.5.46"
	case "d07390561fa64f2041648b755152f7a4eab621e96ba095991721065aa0084a45":
		return "v0.5.47"
	case "b4d58ff4d03bbb79e7d1894b788e5c7fdbc87df53b4bc666c3f37765d3c700ba":
		return "v0.5.48"
	case "7ba8f27d91c1a11cc0743b3007aa1e5f6b3d0c423a04f358732217765e972892":
		return "v0.5.49"
	case "62089961c71c054f6b65df38a95242ff3e5e22f5a103636d20c31916724a9b7f":
		return "v0.5.50"
	case "16c1cb937548a98f1c28dbdd1e03e7de2d0c05dcfbbb7e41f0a4ce30585b6be0":
		return "v0.5.51"
	case "b925bfa837528771048d8dafbe437e3a18881e6c1455d7c6504f6feb45f7f05c":
		return "v0.5.52"
	case "e64c27bf6423b9796a3307b8f9e1ecd966189c47f462d0eefe9c5a22e991c7c2":
		return "v0.5.53"
	case "07cd9803a5ad7ce9aa6bdf4316f2a38e77bfc2c5fb881848bebb0c32813bcc0a":
		return "v0.5.54"
	case "ed87f6d6ec1225f31eebf3fe1fcd46d8759c8503926338c417a29f2cb0e7f94d":
		return "v0.5.55"
	case "cf2a4869e99863a91a2239905b0377b071b5f4dac5f23d60fb0ab49d8fe61771":
		return "v0.5.56"
	case "184ef414a6d127deb89a3a66ac4f614b4245102601c000000997bcf96191580c":
		return "v0.5.57"
	case "b8a77718ec1dcd6c68f21dfe843d3733be7e338dd324d1e0ba782f457e934233":
		return "v0.5.58"
	case "70bafbae4991ffd31c51927f3559dc20409ec519651294e96c04807041310ff6":
		return "v0.5.59"
	case "92c7934e6b9b25666301de51c02526bcd217e5a4e63f470e3c89184fc7b40444":
		return "v0.5.60"
	case "a596047c4c5510d62d925668aa99a2be9f5be9b0526854fdb9374c59a6bec37c":
		return "v0.5.61"
	case "934a75e1f5be70c2afed9b4fab508ea390786a3850323bea29cc64991f9603f2":
		return "v0.5.62"
	case "61a3e2b9d176c60f1a9c0f9225f0bfd13c7c3c1a2bc1c6082a45c70d61a6020c":
		return "v0.5.63"
	case "fba89056e4c7ff23775a3b059e9aa9d55c7df3bb37a6672433a96bb5d0e9c514":
		return "v0.5.64"
	default:
		return ""
	}
}

func legacyUpdateHash(hash [sha256.Size]byte) bool {
	return legacyUpdateRelease(hash) != ""
}

// Allowlist membership alone is insufficient: a different accepted release
// must not impersonate the exact parent whose rollback snapshot we hold.
func legacyUpdateParentHash(actual, expected [sha256.Size]byte) bool {
	return actual == expected && legacyUpdateHash(actual)
}
