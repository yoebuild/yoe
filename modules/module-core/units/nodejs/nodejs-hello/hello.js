// Minimal demo for the nodejs_app yoe class.
//
// Invoked on the target by /usr/bin/nodejs-hello, which runs this script
// through node with the package's bundled node_modules on Node's module
// resolution path. The `require("figlet")` only resolves because that
// node_modules tree lives next to this file under
// /usr/lib/node-apps/nodejs-hello -- a plain `node -e 'require("figlet")'`
// from a shell will fail, which is exactly what makes the app directory
// the unit of distribution.
const figlet = require("figlet");

const argv = process.argv.slice(2);
const text = argv.length > 0 ? argv.join(" ") : "Hello, yoe!";

console.log(figlet.textSync(text, { font: "Slant" }));
