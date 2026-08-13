package main

// These fingerprints bind locally owned lineage artifacts to the exact source
// content compiled into the reviewed product. The companion test recomputes
// every digest from source, so changing implementation content without rotating
// the manifest identity fails the build instead of silently preserving a label.
// This constants-only file is deliberately excluded from the engine digest to
// avoid a self-referential hash.
const (
	sessionStoreSourceSHA256      = "e47b03cc7417dd2ddbda532a0514cf7bcdfbdd46575042d20dee8d2a7c37c10c"
	gsiServerSourceSHA256         = "05a1252c93e075a0543a3cf8074ddbeaa7eb65a772bdd6fe7483482ac28b3b4a"
	contractsSourceSHA256         = "cb8513c20b816b681e083af043fb4ab13ea765b40b9dc055439e197c6644a8f9"
	liveMappingSourceSHA256       = "ea481ddba7f714d2f25248d696d0b4d65a8a23b3b901754f96180053fcb1b8a8"
	presentationCatalogSHA256     = "643bbab16fe6576f5be16ee0e71127a58c799730e6754b5ef99178fd91693183"
	productMainSourceSHA256       = "4f27a6161aef72021063846b6039fc36e99276b995f5490530f4c0b953d9b572"
	productPortsSourceSHA256      = "fcb2526226036afd32051f6ee9f61437fb08911b457580fa9cea874dbf0a5e6f"
	productRecoverySourceSHA256   = "407aa8714c3d7d3772605fa4f6f23e70b3d6249e9870b54fa9d05e5915337501"
	productRuntimeSourceSHA256    = "7100d574f644e537f52be8e6d62f0e3dcbf9842bc1523f63ab178f112c578248"
	productLineageSourceSHA256    = "779dd1a40fed27e012d2df278da6a0865d14b02bfa60e395304837a8db35192e"
	insightEngineSourceSHA256     = "ed03607f7d06de0b5ec3c888d16c143762d0c64a39f5611ba5fcc28cef3fdc52"
	policyEngineSourceSHA256      = "63bb5fce0e5d71195c5a73fe55fb83f2cbc3476fb8cccaf07b15b595351d6f83"
	policyApplicationSourceSHA256 = "5678bc191e6e454694662488b4aa3af9950c1c14299aa70bc4babd3c84a6d771"
)
