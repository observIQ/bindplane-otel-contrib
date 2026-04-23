# Microsoft Sentinel ASIM Examples

Reference artifacts for routing OCSF-normalized logs to Microsoft Sentinel's native ASIM (Advanced Security Information Model) tables via the `azureloganalyticsexporter` and `sentinel_standardization` processor.

## Contents

### `dcrs/`

ARM (Azure Resource Manager) deployment templates that create one Data Collection Rule per ASIM physical table. Each DCR declares a custom input stream matching the ASIM schema, and uses `outputStream: Microsoft-ASim<Schema>Logs` to route ingested rows directly to the native ASIM table (no custom `_CL` table created).

| Template | Target Native Table |
| --- | --- |
| `dcr-asim-auth.json` | `ASimAuthenticationEventLogs` |
| `dcr-asim-network.json` | `ASimNetworkSessionLogs` |
| `dcr-asim-dns.json` | `ASimDnsActivityLogs` |
| `dcr-asim-process.json` | `ASimProcessEventLogs` |
| `dcr-asim-file.json` | `ASimFileEventLogs` |
| `dcr-asim-audit.json` | `ASimAuditEventLogs` |
| `dcr-asim-web.json` | `ASimWebSessionLogs` |
| `dcr-asim-dhcp.json` | `ASimDhcpEventLogs` |
| `dcr-asim-registry.json` | `ASimRegistryEventLogs` |
| `dcr-asim-usermgmt.json` | `ASimUserManagementActivityLogs` |

Parameters are placeholder-valued (`<YOUR_SUBSCRIPTION_ID>`, `<YOUR_RESOURCE_GROUP>`, `<YOUR_WORKSPACE_NAME>`, `<YOUR_DCE_NAME>`). Replace at deploy time or supply via `--parameters`.

### `fixtures/`

Sample OCSF-formatted log records, one per class, used for local/integration testing. Each payload matches the class mapped by the corresponding public blueprint in the `bindplane-op-enterprise` repo (`resources/public/blueprints/ocsf_to_asim_*_bundle.yaml`).

## Deploying a DCR

Prerequisites:
- Azure CLI authenticated as a user/principal with `Contributor` on the target resource group
- An existing Log Analytics workspace and Data Collection Endpoint (DCE) in the same region

Example:

```bash
az deployment group create \
  --resource-group <YOUR_RESOURCE_GROUP> \
  --template-file examples/sentinel-asim/dcrs/dcr-asim-auth.json \
  --parameters \
      workspaceResourceId="/subscriptions/<SUB>/resourcegroups/<RG>/providers/microsoft.operationalinsights/workspaces/<WS>" \
      endpointResourceId="/subscriptions/<SUB>/resourceGroups/<RG>/providers/Microsoft.Insights/dataCollectionEndpoints/<DCE>"
```

The output includes `immutableId` (use as `rule_id` in the exporter config) and `dcrId` (ARM resource ID).

Grant the Bindplane service principal `Monitoring Metrics Publisher` on the resulting DCR:

```bash
az role assignment create \
  --assignee <BINDPLANE_SP_OBJECT_ID> \
  --role "Monitoring Metrics Publisher" \
  --scope <DCR_RESOURCE_ID>
```

## End-to-end flow

```
source logs
  → ocsfstandardizationprocessor          (in bindplane-otel-contrib)
  → OCSF-shaped body
  → ocsf_to_asim_<schema>_bundle blueprint (in bindplane-op-enterprise)
      ├─ transform processor: OCSF fields → ASIM columns in body
      └─ sentinel_standardization processor: sets attributes.sentinel_stream_name
  → azureloganalyticsexporter
      └─ groups by (rule_id, stream_name), Uploads to matching DCR
  → Azure DCR (ARM-deployed from this directory)
      └─ transformKql: source, outputStream: Microsoft-ASim<Schema>Logs
  → native ASIM table (queryable directly or via ASIM union parsers)
```

These DCR templates are one-time Azure-side setup. They are not read by the collector at runtime. The collector only needs the DCE endpoint URL, DCR immutable ID, and stream name (all produced by or derivable from the deployment).
