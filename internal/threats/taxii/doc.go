// Package taxii provides a TAXII 2.1 client and server. The client fetches and pushes
// STIX content to/from a TAXII server (port of OASIS cti-taxii-client). The server
// is an exchange that receives and validates auth and serves TAXII 2.1 endpoints.
package taxii

// MediaTypeTAXII21 is the TAXII 2.1 Accept and Content-Type value.
const MediaTypeTAXII21 = "application/taxii+json;version=2.1"
