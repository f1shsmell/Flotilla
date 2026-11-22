// Rasterizes the brand mark into the application icon. The mark is the one
// source (src/assets/brand-mark.svg); a kit update replaces that file and this
// script is run again, so no second drawing exists to drift.
//
// The .ico embeds PNG images, which Windows has accepted since Vista, at the
// sizes the shell picks between; the 256px layer must be the PNG one.
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { Resvg } from "@resvg/resvg-js";

const here = import.meta.dirname;
const gui = path.resolve(here, "..");
const mark = readFileSync(path.join(gui, "src", "assets", "brand-mark.svg"), "utf8");
const outDir = path.join(gui, "build");

const sizes = [16, 24, 32, 48, 64, 128, 256];
const pngs = sizes.map((size) => ({
  size,
  png: new Resvg(mark, { fitTo: { mode: "width", value: size } }).render().asPng(),
}));

const header = Buffer.alloc(6);
header.writeUInt16LE(0, 0);
header.writeUInt16LE(1, 2);
header.writeUInt16LE(pngs.length, 4);

const directory = Buffer.alloc(pngs.length * 16);
let offset = header.length + directory.length;
pngs.forEach(({ size, png }, index) => {
  const entry = index * 16;
  // 0 stands for 256 in the width and height bytes.
  directory.writeUInt8(size === 256 ? 0 : size, entry);
  directory.writeUInt8(size === 256 ? 0 : size, entry + 1);
  directory.writeUInt16LE(1, entry + 4);
  directory.writeUInt16LE(32, entry + 6);
  directory.writeUInt32LE(png.length, entry + 8);
  directory.writeUInt32LE(offset, entry + 12);
  offset += png.length;
});

mkdirSync(outDir, { recursive: true });
writeFileSync(path.join(outDir, "icon.ico"), Buffer.concat([header, directory, ...pngs.map((p) => p.png)]));
console.log(`icon.ico: ${pngs.length} sizes from brand-mark.svg`);
