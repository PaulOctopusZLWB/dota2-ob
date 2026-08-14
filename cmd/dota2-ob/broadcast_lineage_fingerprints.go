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
	productMainSourceSHA256       = "658cc654f9284889a07dc9c49a070529283f60b013b526544fc5a3fedb503c5f"
	productPortsSourceSHA256      = "238b095c9a9196feb438d3180caccf302f3eb646ecf7a21e9ee3c048ea3d3d66"
	productRecoverySourceSHA256   = "a8600fb5ebb6acf74e96dec17219effc4bf89c9cc6fca126a475f72e0da602c0"
	productRuntimeSourceSHA256    = "6c9c390d3e72596a95d7c0ae7189ed2bf77c1fbc93ddc5342d6bcfaa2f055410"
	productLineageSourceSHA256    = "426f51938697789b97f7599339ba73585d3e64684a444320493b7d4a9eb5e572"
	sessionHighWaterSourceSHA256  = "8cd02d084a5ae7077364e5c93872b2aa00a0f13ae0fbaa9e20d1abdf5a3263c7"
	sessionFollowerSourceSHA256   = "022ac146cdea343cd3c438eab8be585862874914d33fb16e765edbbdd2a8f435"
	insightEngineSourceSHA256     = "03fe0d238c1bb1eb166414e1baa060362356fcdfe964cc968808107e63e7f55f"
	policyEngineSourceSHA256      = "f73d242b74f06589cc68d00592eb1dce6c940883ab6da7320d3ac9d24e06697f"
	policyApplicationSourceSHA256 = "5678bc191e6e454694662488b4aa3af9950c1c14299aa70bc4babd3c84a6d771"
)
