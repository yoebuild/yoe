// Minimal demo for the bun_app yoe class.
//
// Invoked on the target by /usr/bin/bun-hello, which runs this script
// through bun with the package's bundled node_modules on the resolver's
// path. The `import figlet from "figlet"` only resolves because that
// node_modules tree lives next to this file under
// /usr/lib/bun-apps/bun-hello -- a plain `bun -e 'import("figlet")'`
// from a shell will fail, which is exactly what makes the app directory
// the unit of distribution.
//
// Bun runs the .ts file directly with no separate compile step.
import figlet from "figlet";

const argv = process.argv.slice(2);
const text = argv.length > 0 ? argv.join(" ") : "Hello, yoe!";

console.log(figlet.textSync(text, { font: "Slant" }));
console.log(`(bun ${Bun.version})`);
