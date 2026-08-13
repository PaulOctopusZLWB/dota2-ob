package main

// These fingerprints bind locally owned lineage artifacts to the exact source
// content compiled into the reviewed product. The companion test recomputes
// every digest from source, so changing implementation content without rotating
// the manifest identity fails the build instead of silently preserving a label.
// This constants-only file is deliberately excluded from the engine digest to
// avoid a self-referential hash.
const (
	sessionStoreSourceSHA256      = "1d767c57f794eec3e700ad47b91ccba2ec76a0126224c941f63852303e9d8c01"
	liveProjectorSourceSHA256     = "2679981e43eabf6ff0485407701fff88e60db2199b4cab84c9ed6dc7c988312d"
	gsiServerSourceSHA256         = "05a1252c93e075a0543a3cf8074ddbeaa7eb65a772bdd6fe7483482ac28b3b4a"
	contractsSourceSHA256         = "cb8513c20b816b681e083af043fb4ab13ea765b40b9dc055439e197c6644a8f9"
	liveMappingSourceSHA256       = "076eeec63c31e4d775714566ac465ac1b4e073cff092cbedf7ae01ad71a868f3"
	presentationCatalogSHA256     = "643bbab16fe6576f5be16ee0e71127a58c799730e6754b5ef99178fd91693183"
	productMainSourceSHA256       = "4f27a6161aef72021063846b6039fc36e99276b995f5490530f4c0b953d9b572"
	productPortsSourceSHA256      = "fcb2526226036afd32051f6ee9f61437fb08911b457580fa9cea874dbf0a5e6f"
	productRecoverySourceSHA256   = "69de9c37226c80255c8a1b2cd7c2e9b69cb217f664c5330f05028e7e0d48d8ad"
	productRuntimeSourceSHA256    = "6bb76bb71a6e0a852de12444f8fd85e63556e116d4696bb1c0e3824d0d3a6fb4"
	productLineageSourceSHA256    = "744393f19db8bd2d88db4ad1bf4fe63ef200bf24d2c44db02dd055706f688c95"
	insightEngineSourceSHA256     = "ed03607f7d06de0b5ec3c888d16c143762d0c64a39f5611ba5fcc28cef3fdc52"
	policyEngineSourceSHA256      = "63bb5fce0e5d71195c5a73fe55fb83f2cbc3476fb8cccaf07b15b595351d6f83"
	policyApplicationSourceSHA256 = "5678bc191e6e454694662488b4aa3af9950c1c14299aa70bc4babd3c84a6d771"
)
