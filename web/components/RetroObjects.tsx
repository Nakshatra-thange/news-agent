// Hand-drawn retro objects in a soft "clay" style: flat SVG shapes with a
// radial highlight each. Decorative only (aria-hidden), no image files.
// Gradient ids are prefixed per instance, so an object can appear more than
// once on a page (hero, intro, footer) without id clashes.

const tones = {
  blue: ["#eef7fd", "#a9cfeb", "#5f8fbd"],
  green: ["#eef9e6", "#add99e", "#62a05c"],
  orange: ["#fff0dc", "#ffb479", "#dc7234"],
  beige: ["#fffaf0", "#efd6a8", "#bb9157"],
  coral: ["#ffc6b2", "#f06b45", "#b23d1c"],
  cream: ["#ffffff", "#f8eedb", "#d6bf98"],
} as const;

type Tone = keyof typeof tones;

function Shade({ id, tone }: { id: string; tone: Tone }) {
  const [hi, mid, lo] = tones[tone];
  return (
    <radialGradient id={id} cx="35%" cy="28%" r="80%">
      <stop offset="0" stopColor={hi} />
      <stop offset="0.5" stopColor={mid} />
      <stop offset="1" stopColor={lo} />
    </radialGradient>
  );
}

type Props = { id: string; className?: string };

function Frame({ className, children }: { className?: string; children: React.ReactNode }) {
  return (
    <svg className={className} viewBox="0 0 120 120" aria-hidden="true" focusable="false">
      <ellipse cx="60" cy="110" rx="34" ry="5" fill="#000" opacity="0.08" />
      {children}
    </svg>
  );
}

export function MushroomLamp({ id, className }: Props) {
  return (
    <Frame className={className}>
      <defs>
        <Shade id={`${id}-a`} tone="blue" />
      </defs>
      <ellipse cx="60" cy="104" rx="24" ry="6" fill={`url(#${id}-a)`} />
      <rect x="54" y="52" width="12" height="52" rx="6" fill={`url(#${id}-a)`} />
      <path d="M18 58 Q18 18 60 18 Q102 18 102 58 Q60 66 18 58 Z" fill={`url(#${id}-a)`} />
    </Frame>
  );
}

export function RetroTV({ id, className }: Props) {
  return (
    <Frame className={className}>
      <defs>
        <Shade id={`${id}-a`} tone="orange" />
        <Shade id={`${id}-b`} tone="blue" />
        <Shade id={`${id}-c`} tone="coral" />
      </defs>
      <path d="M60 32 L42 10 M60 32 L78 10" stroke="#8a5a3c" strokeWidth="2.5" strokeLinecap="round" />
      <circle cx="42" cy="10" r="4" fill={`url(#${id}-c)`} />
      <circle cx="78" cy="10" r="4" fill={`url(#${id}-c)`} />
      <path d="M34 92 L28 106 M86 92 L92 106" stroke="#8a5a3c" strokeWidth="4" strokeLinecap="round" />
      <rect x="16" y="30" width="88" height="64" rx="20" fill={`url(#${id}-a)`} />
      <rect x="26" y="40" width="52" height="44" rx="13" fill={`url(#${id}-b)`} />
      <path d="M32 50 Q40 44 50 46" stroke="#fff" strokeWidth="3" strokeLinecap="round" fill="none" opacity="0.7" />
      <circle cx="91" cy="51" r="6" fill={`url(#${id}-c)`} />
      <circle cx="91" cy="70" r="6" fill={`url(#${id}-c)`} />
    </Frame>
  );
}

export function TuneBox({ id, className }: Props) {
  return (
    <Frame className={className}>
      <defs>
        <Shade id={`${id}-a`} tone="beige" />
        <Shade id={`${id}-b`} tone="coral" />
      </defs>
      <rect x="20" y="24" width="80" height="78" rx="16" fill={`url(#${id}-a)`} />
      <circle cx="60" cy="63" r="27" fill={`url(#${id}-b)`} />
      <circle cx="60" cy="63" r="15" fill="#c8512d" />
      <circle cx="60" cy="63" r="6" fill={`url(#${id}-b)`} />
      <circle cx="88" cy="34" r="3.5" fill="#c8512d" />
    </Frame>
  );
}

export function EggChair({ id, className }: Props) {
  return (
    <Frame className={className}>
      <defs>
        <Shade id={`${id}-a`} tone="green" />
        <Shade id={`${id}-b`} tone="cream" />
      </defs>
      <path d="M48 88 L38 108 M72 88 L82 108" stroke="#4f7f49" strokeWidth="4.5" strokeLinecap="round" />
      <path d="M22 66 C16 30 44 12 60 12 C76 12 104 30 98 66 C94 92 26 92 22 66 Z" fill={`url(#${id}-a)`} />
      <path d="M34 62 C32 38 48 28 60 28 C72 28 88 38 86 62 Z" fill="#5c9656" opacity="0.55" />
      <ellipse cx="60" cy="68" rx="30" ry="12" fill={`url(#${id}-b)`} />
    </Frame>
  );
}

export function GlobeLamp({ id, className }: Props) {
  return (
    <Frame className={className}>
      <defs>
        <Shade id={`${id}-a`} tone="orange" />
      </defs>
      <path d="M60 66 L38 108 M60 66 L82 108 M60 66 L60 106" stroke="#a87650" strokeWidth="3" strokeLinecap="round" />
      <circle cx="60" cy="40" r="30" fill={`url(#${id}-a)`} />
      <g stroke="#c96b2f" strokeWidth="1.6" fill="none" opacity="0.6">
        <ellipse cx="60" cy="40" rx="30" ry="10" />
        <ellipse cx="60" cy="40" rx="12" ry="30" />
        <path d="M30 40 H90" />
      </g>
    </Frame>
  );
}

export function WaveSeat({ id, className }: Props) {
  return (
    <Frame className={className}>
      <defs>
        <Shade id={`${id}-a`} tone="blue" />
      </defs>
      <path d="M30 80 L28 102 M90 80 L92 102 M52 84 L50 104 M70 84 L72 104" stroke="#5f8fbd" strokeWidth="9" strokeLinecap="round" />
      <path
        d="M14 56 C14 38 30 34 38 46 C44 56 50 56 56 46 C62 36 72 34 80 44 C86 52 92 50 96 42 C104 30 110 44 106 62 C102 84 82 88 60 88 C34 88 14 80 14 56 Z"
        fill={`url(#${id}-a)`}
      />
    </Frame>
  );
}

export function BallTree({ id, className }: Props) {
  const balls: [number, number, Tone][] = [
    [60, 18, "coral"],
    [32, 32, "green"],
    [88, 30, "orange"],
    [44, 54, "blue"],
    [78, 52, "beige"],
  ];
  return (
    <Frame className={className}>
      <defs>
        <Shade id={`${id}-coral`} tone="coral" />
        <Shade id={`${id}-green`} tone="green" />
        <Shade id={`${id}-orange`} tone="orange" />
        <Shade id={`${id}-blue`} tone="blue" />
        <Shade id={`${id}-beige`} tone="beige" />
      </defs>
      <g stroke="#5f8fbd" strokeWidth="3" strokeLinecap="round" fill="none">
        <path d="M60 104 V18 M60 78 Q32 70 32 32 M60 70 Q88 64 88 30 M60 88 Q44 80 44 54 M60 84 Q78 76 78 52" />
      </g>
      <ellipse cx="60" cy="104" rx="20" ry="5" fill={`url(#${id}-blue)`} />
      {balls.map(([cx, cy, tone]) => (
        <circle key={`${cx}-${cy}`} cx={cx} cy={cy} r="10" fill={`url(#${id}-${tone})`} />
      ))}
    </Frame>
  );
}

export function RobotHead({ id, className }: Props) {
  return (
    <Frame className={className}>
      <defs>
        <Shade id={`${id}-a`} tone="cream" />
        <Shade id={`${id}-b`} tone="blue" />
        <Shade id={`${id}-c`} tone="coral" />
        <Shade id={`${id}-d`} tone="green" />
      </defs>
      <path d="M60 30 V14" stroke="#a87650" strokeWidth="3" strokeLinecap="round" />
      <circle cx="60" cy="12" r="6" fill={`url(#${id}-c)`} />
      <rect x="10" y="52" width="14" height="24" rx="7" fill={`url(#${id}-d)`} />
      <rect x="96" y="52" width="14" height="24" rx="7" fill={`url(#${id}-d)`} />
      <rect x="20" y="28" width="80" height="72" rx="24" fill={`url(#${id}-a)`} />
      <circle cx="44" cy="58" r="10" fill={`url(#${id}-b)`} />
      <circle cx="76" cy="58" r="10" fill={`url(#${id}-b)`} />
      <circle cx="41" cy="55" r="3" fill="#fff" />
      <circle cx="73" cy="55" r="3" fill="#fff" />
      <path d="M48 80 Q60 90 72 80" stroke="#c8512d" strokeWidth="4" strokeLinecap="round" fill="none" />
      <circle cx="32" cy="76" r="5" fill="#ffb479" opacity="0.7" />
      <circle cx="88" cy="76" r="5" fill="#ffb479" opacity="0.7" />
    </Frame>
  );
}

export function RotaryPhone({ id, className }: Props) {
  return (
    <Frame className={className}>
      <defs>
        <Shade id={`${id}-a`} tone="green" />
        <Shade id={`${id}-b`} tone="cream" />
      </defs>
      <path d="M18 104 L26 58 Q30 46 44 46 H76 Q90 46 94 58 L102 104 Z" fill={`url(#${id}-a)`} />
      <path d="M14 40 Q14 22 32 22 H88 Q106 22 106 40 Q106 46 98 46 H88 Q84 36 76 36 H44 Q36 36 32 46 H22 Q14 46 14 40 Z" fill={`url(#${id}-a)`} />
      <circle cx="60" cy="78" r="19" fill={`url(#${id}-b)`} />
      <g fill="#62a05c">
        <circle cx="60" cy="65" r="3" />
        <circle cx="71" cy="70" r="3" />
        <circle cx="73" cy="82" r="3" />
        <circle cx="49" cy="70" r="3" />
        <circle cx="47" cy="82" r="3" />
        <circle cx="60" cy="78" r="4" />
      </g>
    </Frame>
  );
}

/** The nine objects in the hero showcase, in display order. */
export const showcase = [RetroTV, RobotHead, EggChair, WaveSeat, MushroomLamp, TuneBox, BallTree, GlobeLamp, RotaryPhone];
