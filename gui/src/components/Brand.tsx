// The mark and lockup are static exports of the brand kit's v6 lettering
// (scratch/logo): a kit iteration is a swap of these two asset files, not a
// redraw here. no-inline keeps them real files: the renderer's CSP allows
// 'self' images only, not data: URIs.
import lockup from "../assets/brand-lockup.svg?no-inline";
import mark from "../assets/brand-mark.svg?no-inline";
export function BrandMark(): React.ReactElement {
  return <img className="brand-mark" src={mark} width={28} height={28} alt="" />;
}

// The lockup names the product in the empty states, so it carries the name as
// its alt text rather than hiding as decoration.
export function BrandLockup(): React.ReactElement {
  return <img className="brand-lockup" src={lockup} alt="Flotilla" />;
}
