# Modernize Azure Pipelines tasks (`modernize-ado`)

Status: draft, not approved. Normative once approved.

This specification defines how `modernize` treats the 73 built-in task
versions that Microsoft retires on 2026-10-15 (removed 2027-03-15), listed in
`catalog/azure-pipelines/tasks.retirement.yaml`
([official announcement](https://learn.microsoft.com/en-us/azure/devops/pipelines/tasks/task-retirement)).
`modernize` upgrades Azure Pipelines in place and never changes platform.

## 1. Classes

Every retired task version has exactly one class.

| Class | Meaning | `modernize` behavior |
|---|---|---|
| `auto` | One replacement; every input of the old version exists in the replacement with the same type and nothing new is required. | Rewrite the version in place; keep the inputs. Fidelity `exact`. |
| `map` | One replacement; inputs differ. | Rewrite only through a documented input mapping in `catalog/azure-pipelines/mappings`. Without a mapping the step is `needs-manual`. |
| `manual` | Several replacements, or a choice a person must make. | Never rewritten. `needs-manual` diagnostic naming the candidates. |
| `none` | No replacement task from Microsoft. | Never rewritten. `unsupported` diagnostic; the step is left in place. |
| `unverified` | Input facts of the retired version are not in the catalog. | Treated as `map` without a mapping until verified from an official source. |

`modernize` MUST NOT guess input renames. A mapping entry MUST cite the
official page that documents both task versions.

## 2. Evidence rules

1. A class other than `unverified` may only be set from catalog facts or from a
   cited official page.
2. The classification in section 3 was computed from
   `tasks.generated.yaml` and `tasks.retirement.yaml` only. It never reads
   Microsoft prose. Moving a task out of `unverified`, or from `auto` to
   `map`, requires a source and a new vector; it MUST NOT silently change an
   existing vector.
3. VERIFY: `auto` means input *names and types* match. It does not prove
   identical behavior; each `auto` entry still needs its official release
   notes read before implementation.

## 3. Task table

Counts: auto 4, manual 3, map 18, none 14, unverified 34. Total 73.

Test vectors:

| ID | Task | Class | Replacement | Basis |
|---|---|---|---|---|
| MODADO-001 | `AndroidBuild@1` | unverified | `Gradle@4` | Input facts for the retired version are not in the catalog. |
| MODADO-002 | `AndroidSigning@1` | unverified | `AndroidSigning@3` | Input facts for the retired version are not in the catalog. |
| MODADO-003 | `AndroidSigning@2` | map | `AndroidSigning@3` | 6 input(s) absent in the replacement. |
| MODADO-004 | `AppCenterDistribute@0` | none | - | No Microsoft replacement task is listed. |
| MODADO-005 | `AppCenterDistribute@1` | none | - | No Microsoft replacement task is listed. |
| MODADO-006 | `AppCenterDistribute@2` | none | - | No Microsoft replacement task is listed. |
| MODADO-007 | `ArchiveFiles@1` | unverified | `ArchiveFiles@2` | Input facts for the retired version are not in the catalog. |
| MODADO-008 | `AutomatedAnalysis@0` | none | - | No Microsoft replacement task is listed. |
| MODADO-009 | `AzureCLI@0` | unverified | `AzureCLI@3` | Input facts for the retired version are not in the catalog. |
| MODADO-010 | `AzureCloudPowerShellDeployment@1` | map | `AzurePowerShell@5` | 22 input(s) absent in the replacement; new required: ConnectedServiceNameARM, CustomTargetAzurePs. |
| MODADO-011 | `AzureCloudPowerShellDeployment@2` | map | `AzurePowerShell@5` | 15 input(s) absent in the replacement; new required: ConnectedServiceNameARM, CustomTargetAzurePs. |
| MODADO-012 | `AzureFunction@0` | unverified | `AzureFunction@2` | Input facts for the retired version are not in the catalog. |
| MODADO-013 | `AzureMonitor@0` | map | `AzureMonitor@1` | 3 input(s) absent in the replacement; new required: resource, alertRule. |
| MODADO-014 | `AzureNLBManagement@1` | manual | `AzureCLI@3`, `AzurePowerShell@5` | Replaced by several tasks; the split needs a person. |
| MODADO-015 | `AzurePowerShell@1` | unverified | `AzurePowerShell@5` | Input facts for the retired version are not in the catalog. |
| MODADO-016 | `AzureResourceGroupDeployment@1` | unverified | `AzureResourceGroupDeployment@2` | Input facts for the retired version are not in the catalog. |
| MODADO-017 | `AzureRmWebAppDeployment@2` | unverified | `AzureRmWebAppDeployment@5` | Input facts for the retired version are not in the catalog. |
| MODADO-018 | `AzureWebPowerShellDeployment@1` | unverified | `AzureRmWebAppDeployment@5` | Input facts for the retired version are not in the catalog. |
| MODADO-019 | `CacheBeta@0` | map | `Cache@2` | type changed: key. |
| MODADO-020 | `Chef@1` | none | - | No Microsoft replacement task is listed. |
| MODADO-021 | `ChefKnife@1` | none | - | No Microsoft replacement task is listed. |
| MODADO-022 | `CmdLine@1` | unverified | `CmdLine@2` | Input facts for the retired version are not in the catalog. |
| MODADO-023 | `CondaEnvironment@1` | none | - | No Microsoft replacement task is listed. |
| MODADO-024 | `CopyFiles@1` | unverified | `CopyFiles@2` | Input facts for the retired version are not in the catalog. |
| MODADO-025 | `CopyPublishBuildArtifacts@1` | manual | `CopyFiles@2`, `PublishBuildArtifacts@1` | Replaced by several tasks; the split needs a person. |
| MODADO-026 | `cURLUploader@1` | unverified | `cURLUploader@2` | Input facts for the retired version are not in the catalog. |
| MODADO-027 | `DeployVisualStudioTestAgent@1` | unverified | `VSTest@3` | Input facts for the retired version are not in the catalog. |
| MODADO-028 | `DotNetCoreCLI@0` | unverified | `DotNetCoreCLI@2` | Input facts for the retired version are not in the catalog. |
| MODADO-029 | `DotNetCoreCLI@1` | unverified | `DotNetCoreCLI@2` | Input facts for the retired version are not in the catalog. |
| MODADO-030 | `DotNetCoreInstaller@0` | auto | `UseDotNet@2` | Every input exists in the replacement with the same type. |
| MODADO-031 | `DotNetCoreInstaller@1` | auto | `UseDotNet@2` | Every input exists in the replacement with the same type. |
| MODADO-032 | `DownloadPackage@0` | auto | `DownloadPackage@1` | Every input exists in the replacement with the same type. |
| MODADO-033 | `DownloadPipelineArtifact@0` | map | `DownloadPipelineArtifact@2` | type changed: pipelineid, artifactname, targetpath; new required: project, pipeline, runId. |
| MODADO-034 | `DuffleInstaller@0` | none | - | No Microsoft replacement task is listed. |
| MODADO-035 | `FtpUpload@1` | map | `FtpUpload@2` | 1 input(s) absent in the replacement. |
| MODADO-036 | `GitHubRelease@0` | map | `GitHubRelease@1` | 2 input(s) absent in the replacement; type changed: githubconnection. |
| MODADO-037 | `Gradle@1` | unverified | `Gradle@4` | Input facts for the retired version are not in the catalog. |
| MODADO-038 | `InstallAppleCertificate@0` | unverified | `InstallAppleCertificate@2` | Input facts for the retired version are not in the catalog. |
| MODADO-039 | `InstallAppleCertificate@1` | unverified | `InstallAppleCertificate@2` | Input facts for the retired version are not in the catalog. |
| MODADO-040 | `InstallAppleProvisioningProfile@0` | unverified | `InstallAppleProvisioningProfile@1` | Input facts for the retired version are not in the catalog. |
| MODADO-041 | `InvokeRESTAPI@0` | unverified | `InvokeRESTAPI@1` | Input facts for the retired version are not in the catalog. |
| MODADO-042 | `JenkinsQueueJob@1` | unverified | `JenkinsQueueJob@2` | Input facts for the retired version are not in the catalog. |
| MODADO-043 | `Maven@1` | unverified | `Maven@4` | Input facts for the retired version are not in the catalog. |
| MODADO-044 | `Maven@2` | auto | `Maven@4` | Every input exists in the replacement with the same type. |
| MODADO-045 | `MysqlDeploymentOnMachineGroup@1` | none | - | No Microsoft replacement task is listed. |
| MODADO-046 | `NuGet@0` | map | `NuGetCommand@2` | type changed: command; new required: feedPublish, externalEndpoint, versionEnvVar. |
| MODADO-047 | `NuGetInstaller@0` | manual | `NuGetCommand@2`, `NuGetToolInstaller@1` | Replaced by several tasks; the split needs a person. |
| MODADO-048 | `NuGetPackager@0` | map | `NuGetCommand@2` | 4 input(s) absent in the replacement; new required: feedPublish, externalEndpoint, arguments. |
| MODADO-049 | `NuGetPublisher@0` | map | `NuGetCommand@2` | 8 input(s) absent in the replacement; new required: feedPublish, externalEndpoint, versionEnvVar, arguments. |
| MODADO-050 | `NuGetRestore@1` | map | `NuGetCommand@2` | 2 input(s) absent in the replacement; new required: feedPublish, externalEndpoint, versionEnvVar, arguments. |
| MODADO-051 | `PackerBuild@0` | map | `PackerBuild@1` | new required: managedImageName. |
| MODADO-052 | `PowerShell@1` | unverified | `PowerShell@2` | Input facts for the retired version are not in the catalog. |
| MODADO-053 | `PowerShellOnTargetMachines@1` | map | `PowerShellOnTargetMachines@3` | 8 input(s) absent in the replacement; new required: Machines. |
| MODADO-054 | `PublishSymbols@1` | unverified | `PublishSymbols@2` | Input facts for the retired version are not in the catalog. |
| MODADO-055 | `PublishToAzureServiceBus@0` | unverified | `PublishToAzureServiceBus@2` | Input facts for the retired version are not in the catalog. |
| MODADO-056 | `PyPIPublisher@0` | map | `TwineAuthenticate@1` | 6 input(s) absent in the replacement. |
| MODADO-057 | `QuickPerfTest@1` | unverified | `AzureLoadTest@1` | Input facts for the replacement are not in the catalog. |
| MODADO-058 | `RunVisualStudioTestsusingTestAgent@1` | unverified | `VSTest@3` | Input facts for the retired version are not in the catalog. |
| MODADO-059 | `ServiceFabricComposeDeploy@0` | map | `ServiceFabricDeploy@1` | 8 input(s) absent in the replacement; type changed: serviceconnectionname, dockerregistryconnection; new required: applicationPackagePath, dockerRegistryEndpoint. |
| MODADO-060 | `ServiceFabricUpdateAppVersions@1` | unverified | `ServiceFabricUpdateManifests@2` | Input facts for the retired version are not in the catalog. |
| MODADO-061 | `SonarQubePostTest@1` | none | - | No Microsoft replacement task is listed. |
| MODADO-062 | `SonarQubePreBuild@1` | none | - | No Microsoft replacement task is listed. |
| MODADO-063 | `VSMobileCenterTest@0` | none | - | No Microsoft replacement task is listed. |
| MODADO-064 | `XamarinAndroid@1` | map | `DotNetCoreCLI@2` | 23 input(s) absent in the replacement; type changed: outputdir; new required: custom, feedPublish, externalEndpoint, versionEnvVar. |
| MODADO-065 | `XamarinComponentRestore@0` | unverified | `DotNetCoreCLI@2` | Input facts for the retired version are not in the catalog. |
| MODADO-066 | `XamariniOS@1` | unverified | `DotNetCoreCLI@2` | Input facts for the retired version are not in the catalog. |
| MODADO-067 | `XamariniOS@2` | map | `DotNetCoreCLI@2` | 16 input(s) absent in the replacement; new required: custom, feedPublish, externalEndpoint, versionEnvVar. |
| MODADO-068 | `XamarinLicense@1` | none | - | No Microsoft replacement task is listed. |
| MODADO-069 | `XamarinTestCloud@1` | none | - | No Microsoft replacement task is listed. |
| MODADO-070 | `Xcode@2` | unverified | `Xcode@5` | Input facts for the retired version are not in the catalog. |
| MODADO-071 | `Xcode@3` | unverified | `Xcode@5` | Input facts for the retired version are not in the catalog. |
| MODADO-072 | `Xcode@4` | unverified | `Xcode@5` | Input facts for the retired version are not in the catalog. |
| MODADO-073 | `XcodePackageiOS@0` | unverified | `Xcode@5` | Input facts for the retired version are not in the catalog. |

## 4. Open questions

- Retired-version input facts are missing from the catalog for the
  `unverified` rows. Either extend `catalog-gen` to pin them, or read the
  official task reference for each; the maintainer decides.
- `map` rows with many absent inputs (for example the Xamarin and Azure Cloud
  PowerShell tasks) are probably `manual` in practice. They stay `map` here
  until a person reads the sources.
