package main

// These fingerprints bind locally owned lineage artifacts to the exact source
// content compiled into the reviewed product. The companion test recomputes
// every digest from source, so changing implementation content without rotating
// the manifest identity fails the build instead of silently preserving a label.
// This constants-only file is deliberately excluded from the engine digest to
// avoid a self-referential hash.
const (
	contractsSourceSHA256         = "cb8513c20b816b681e083af043fb4ab13ea765b40b9dc055439e197c6644a8f9"
	liveMappingSourceSHA256       = "ea481ddba7f714d2f25248d696d0b4d65a8a23b3b901754f96180053fcb1b8a8"
	presentationCatalogSHA256     = "643bbab16fe6576f5be16ee0e71127a58c799730e6754b5ef99178fd91693183"
	productMainSourceSHA256       = "00628247f7ba01f924af5d1085952e5462e3d0b647cd49f765c143fd32ee0a82"
	productPortsSourceSHA256      = "35ece286e7e03fe4b8ef6d5279784e24c38ef3f9e4331196cfcd451889814166"
	productRecoverySourceSHA256   = "a8600fb5ebb6acf74e96dec17219effc4bf89c9cc6fca126a475f72e0da602c0"
	productRuntimeSourceSHA256    = "6c9c390d3e72596a95d7c0ae7189ed2bf77c1fbc93ddc5342d6bcfaa2f055410"
	productLineageSourceSHA256    = "f9706d5f8fd398f5ff89c73d265345e8b74b7950107f13255b6a2f9cc88d0416"
	insightEngineSourceSHA256     = "ed03607f7d06de0b5ec3c888d16c143762d0c64a39f5611ba5fcc28cef3fdc52"
	policyEngineSourceSHA256      = "63bb5fce0e5d71195c5a73fe55fb83f2cbc3476fb8cccaf07b15b595351d6f83"
	policyApplicationSourceSHA256 = "5678bc191e6e454694662488b4aa3af9950c1c14299aa70bc4babd3c84a6d771"
)
