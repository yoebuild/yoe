# Project name

**(actually, I'm really happy with `yoe`, likely drop this page in the
future.)**

This tool needs a new project name. Feel that since this is a new start, having
something other than "yoe" might be beneficial.

Requirements:

- Clearly indicate it builds/assembles something. Yocto is not a good name.
- Be simple.
- Good if inspired by nature.
- Has a good CLI name as well.

## Analysis

**Strongest existing candidates:**

- **Forge** — implies building/shaping from raw materials. Nature-adjacent
  (metalworking is elemental). CLI: `forge`. Clean, memorable, easy to type.
- **Foundry** — same vein, slightly more "place where things are made." CLI name
  is long though (`foundry` or `fdy`).
- **Crucible** — evocative (vessel where materials transform under heat), but
  harder to spell/type as a CLI name.

**Patterns to avoid:**

- Compound names (EdgeForge, FusionCore, TerraFusion, etc.) read like enterprise
  SaaS products, not dev tools. The best CLI tools have one short word: `make`,
  `nix`, `zig`, `go`, `apt`.
- "Build" in the name is redundant — users already know it's a build tool.
  `make` doesn't say "build" anywhere.
- Fusion/Nexus/Matrix — overused in tech, vague, no build connotation.

**Additional candidates worth considering:**

- **Kiln** — where you fire/harden raw materials into finished form.
  Nature-adjacent (clay, heat). CLI: `kiln`. Four letters, easy to type, not
  taken by anything major.
- **Anvil** — where things get shaped. CLI: `anvil`. Five letters, strong
  imagery.
- **Loom** — weaves threads into fabric, analogous to assembling units into a
  system. CLI: `loom`. Nature-inspired, simple.
- **Grove** — nature-inspired, suggests growing/cultivating a system from parts.
  CLI: `grove`. Softer feel, different from typical build tool names.
- **Cairn** — a stack of stones assembled by hand, marking a path. CLI: `cairn`.
  Nature-inspired, implies deliberate assembly, five letters.

**Ranking against requirements** (build connotation + simple + nature-inspired +
good CLI name): Kiln > Forge > Cairn > Loom > Anvil.

### Domain & GitHub availability (April 2026)

| Name      | Software conflicts                                                                                                                                                                      | Domain availability                                                        | GitHub orgs                             |
| --------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------- | --------------------------------------- |
| **Cairn** | Moderate — cairn-dev/cairn AI coding agent (194 stars, codewithcairn.ai), google/cairn (P4 compiler), wabarc/cairn (npm CLI), cairn-lang orgs taken, Cairn Software (cairnsoftware.com) | cairn.dev, cairn.sh, cairn.io all taken; cairnbuild.dev possibly available | cairnproject, cairn-build available     |
| **Grove** | Many tiny projects, no dominant one                                                                                                                                                     | grove.sh possibly available                                                | Unchecked                               |
| **Kiln**  | Crowded — Kiln AI (4.7k stars), Fog Creek Kiln, pivotal-cf/kiln, kiln.sh secrets manager                                                                                                | kilnbuild.dev, usekiln.dev likely available                                | kilndev, getkiln, kilnproject available |
| **Anvil** | Moderate — anvil.works (Python framework), Foundry `anvil` CLI                                                                                                                          | anvil.dev possibly available                                               | Unchecked                               |
| **Forge** | Hard pass — `forge` CLI is Foundry/Ethereum (17k stars)                                                                                                                                 | All taken                                                                  | All taken                               |
| **Loom**  | Hard pass — Atlassian's $975M video product                                                                                                                                             | All taken                                                                  | All taken                               |

**Revised ranking** (factoring in namespace availability): Grove > Cairn > Anvil
\> Kiln > Forge > Loom.

Cairn is more crowded than initially expected. The AI coding agent
(cairn-dev/cairn) is the biggest concern — same audience, active, funded.

### Vintage trade verbs (April 2026)

Words from woodworking and other traditional trades — verbs that invoke
building:

**Woodworking:** hew (shape by cutting), rive (split along grain), adze (shaping
tool), joinery (connecting pieces)

**Blacksmithing:** temper (strengthen through heat), weld (join pieces), braze
(join metals with heat)

**Masonry/general:** tamp (pack down firmly), plumb (set true/vertical)

**Top candidate: Hew** — 3 letters, strong verb, memorable. "Hew a system from
raw source." However, hew.sh is an active programming language (launched Feb
2026), and core domains (hew.dev, hew.sh, hew.io) are taken.

| Hew combo    | GitHub org | Domain                                      |
| ------------ | ---------- | ------------------------------------------- |
| **hewbuild** | Available  | hew.build, hewbuild.com, hewbuild.dev avail |
| **hewlabs**  | Available  | hewlabs.dev available                       |
| **usehew**   | Available  | usehew.dev available                        |
| **hewdev**   | Available  | Unchecked                                   |
| **hewsys**   | Available  | hewsys.dev available (hewsys.com taken)     |

**hew.build** is the standout — short, descriptive, and the `hewbuild` GitHub
org is free. CLI would still be `hew`.

### Masonry & woodworking — tools and operations

**Masonry tools:** trowel, hawk (handheld mortar platform), scutch (trimming
hammer), bevel, darby (leveling tool)

**Masonry objects:** ashlar (precisely dressed stone block), quoin
(cornerstone), corbel, plinth, sett (cut paving stone), mortar

**Masonry operations:** lay, cope (shape to fit), dress (shape raw stone into
finished blocks), point, shim

**Woodworking operations:** hew, rive (split along grain), pare (shave with
chisel), rout (cut grooves), rabbet, dado (groove across grain), miter, mortise,
tenon, cleave, kerf (the cut made by a saw), chamfer

**Short-list (4 letters, distinctive, good CLI name, building connotation):**

- **Rive** — split raw material along its natural grain. Working _with_ the
  material, not against it.
- **Kerf** — the cut itself. Precise, technical, uncommon in software.
- **Cope** — to shape something to fit. Exactly what a build system does.
- **Dado** — a groove that receives another piece, like a slot units fit into.
- **Hawk** — the mason's platform that holds material while you work. Also a
  nature word (bird). Four letters, sharp, memorable.

**Joinery:** spline (strip joining two pieces), dowel (aligning pin), cleat
(strip holding boards together)

**Timber framing:** scarf (joint splicing timbers into one), truss (rigid
framework of members), brace (diagonal stiffener)

**Cooperage (barrel-making):** stave (shaped plank that forms the barrel), croze
(groove cut for the end piece to sit in)

**Boatbuilding:** plank, strake (continuous line of planking bow to stern)

**Roofing:** batten (strips holding material in place), slate

**Extended short-list:**

- **Stave** — individual pieces shaped to form a whole. Almost too perfect for a
  unit-based build system. Five letters.
- **Truss** — a rigid framework assembled from individual members. Strong,
  structural. Five letters.
- **Scarf** — joining pieces into something continuous. Uncommon, distinctive.
  Five letters.
- **Croze** — the groove that everything fits into. Obscure enough to be
  completely uncontested. Five letters.

### Archery theme

**Arrow components (assembly metaphor):** shaft, fletch (attach feathers to
arrow), nock (notch that clips onto bowstring), vane (stabilizing fin)

**The bow:** stave (a bow starts as a stave), limb, riser (central grip holding
limbs)

**Actions:** draw, loose, nock (to ready an arrow for release)

**Archery short-list:**

- **Fletch** — a verb about assembling precision components (shaft + point +
  nock
  - feathers) into something that flies true. Six letters, distinctive.
- **Nock** — to ready an arrow for release. Four letters, punchy. "Nock and
  release" maps to "build and deploy." Uncommon in software.
- **Stave** — gains weight here too. A bow stave, a barrel stave, a walking
  staff. The universal raw material shaped into something purposeful.

## Ideas

The following should not be taken too seriously, but may suggest ideas:

Build-focused names ZBuild

EdgeBuild

CloudEdge Build

BuildEdge

StreamBuild

SimpBuild

LiteBuild

SwiftBuild

DeviceBuild

Edge / flow / modern dev feel EdgeFlow

EdgeCI

DevEdge

Sleeker / abstract build alternatives EdgeForge

Forge

Foundry

Crucible

Matrix

Nature / frontier / cosmic HorizonBuild

FrontierBuild

NebulaBuild

RidgeBuild

TundraBuild

AuroraBuild

EventBuild

ZenithBuild

Creation / generation themed Gen

EdgeGen

NebulaGen

GenForge

GenCrucible

“Bringing things together” themes Fusion

XFusion

ZFusion

FusionEdge

FusionCore

CoreFusion

StarFusion

AstroFusion

SwiftFusion

LightFusion

TerraFusion

VertexFusion

Other conceptual names Nexus

Mosaic

Constellation / Constella

CLI name ideas xf

xfn

fus

fsn

xfuse
