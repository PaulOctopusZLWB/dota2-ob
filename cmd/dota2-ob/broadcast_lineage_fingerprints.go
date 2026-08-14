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
	productMainSourceSHA256       = "bf57db6ccc69338e44912487e8de2123ff0361217b774d5172951fdc7b7b2286"
	productPortsSourceSHA256      = "4c10434def0a67888f8188f4d0935ebb4ae044616c6f0e49d24d9ba6ccb27290"
	productRecoverySourceSHA256   = "a8600fb5ebb6acf74e96dec17219effc4bf89c9cc6fca126a475f72e0da602c0"
	productRuntimeSourceSHA256    = "6c9c390d3e72596a95d7c0ae7189ed2bf77c1fbc93ddc5342d6bcfaa2f055410"
	productLineageSourceSHA256    = "d3cc3b35c8eecdba4574fdfddca5348942be746e36887f29cf23dc47fb8e0ce6"
	productLiveOnlySourceSHA256   = "61ebd6da3217ed953c854fbae28197690845b088efa16743fd74b724ffe9ccfa"
	productRecoveryV3SourceSHA256 = "bb43da8758ac98e2fe4a8d0959bba79849276d148709f55c855c0fcd8c9c3496"
	productRuntimeV3SourceSHA256  = "1dc7b5b04496549c00c4baf9bae7d1397bc5d1de518ece8a7092ca3ad6a5b963"
	sessionHighWaterSourceSHA256  = "8cd02d084a5ae7077364e5c93872b2aa00a0f13ae0fbaa9e20d1abdf5a3263c7"
	sessionFollowerSourceSHA256   = "022ac146cdea343cd3c438eab8be585862874914d33fb16e765edbbdd2a8f435"
	insightEngineSourceSHA256     = "70e5742a9b6e6fa55c2f8798a5dbb36d0e862f1e7ce3d729c588be6f74c476e9"
	policyEngineSourceSHA256      = "fd1d0f801c3cc43f7dc367ac95083d335082b0bccdd42a35eedf7711c10029af"
	policyApplicationSourceSHA256 = "5678bc191e6e454694662488b4aa3af9950c1c14299aa70bc4babd3c84a6d771"
)
