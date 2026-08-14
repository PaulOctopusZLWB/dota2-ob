package main

// These fingerprints bind locally owned lineage artifacts to the exact source
// content compiled into the reviewed product. The companion test recomputes
// every digest from source, so changing implementation content without rotating
// the manifest identity fails the build instead of silently preserving a label.
// This constants-only file is deliberately excluded from the engine digest to
// avoid a self-referential hash.
const (
	contractsSourceSHA256         = "cb8513c20b816b681e083af043fb4ab13ea765b40b9dc055439e197c6644a8f9"
	presentationCatalogSHA256     = "643bbab16fe6576f5be16ee0e71127a58c799730e6754b5ef99178fd91693183"
	productMainSourceSHA256       = "8eb59af7e57a26dd7d8c8032c0d09893e0c658c65bcf0dc0c2c81050246d1df5"
	productPortsSourceSHA256      = "237eb58895df2698a4ba156ba3a1c2baed729dbecadc85511545175d29107a35"
	productRecoverySourceSHA256   = "a08a18e1fc001594a0a6fc6d0072d3833f6c9be587751d28406dded0424daa43"
	productRuntimeSourceSHA256    = "8503078ce59015d0187c952db909ef71e7b1ebd3a060b2c0c773f35d2a0f6fb3"
	productLineageSourceSHA256    = "457169498fccfbe85ce9a736e4c276c993dcf8a999024515dbc065f6f685788a"
	insightEngineSourceSHA256     = "ed03607f7d06de0b5ec3c888d16c143762d0c64a39f5611ba5fcc28cef3fdc52"
	policyEngineSourceSHA256      = "63bb5fce0e5d71195c5a73fe55fb83f2cbc3476fb8cccaf07b15b595351d6f83"
	policyApplicationSourceSHA256 = "5678bc191e6e454694662488b4aa3af9950c1c14299aa70bc4babd3c84a6d771"
)
