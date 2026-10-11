import "./Intro.css";

/** The cinematic intro: a deep-space "signal acquisition" over a starfield
 *  that resolves into the Synergy artwork, the name, and then the feed.
 *
 *  It is a server component with static markup, rendered above the page on
 *  every route; CSS shows it only while <html data-intro> is "play" or
 *  "leaving", so it never causes a hydration mismatch and the feed renders
 *  and loads underneath it. introInitScript (inlined in <head>) decides
 *  before first paint whether it plays, and ends it. */
export function Intro() {
  return (
    <div className="intro" data-intro-root="">
      <div className="intro-stage" aria-hidden="true">
        <div className="intro-sky">
          <div className="intro-nebula" />
          <div className="intro-drift far">
            <span className="intro-stars far-a" />
            <span className="intro-stars far-b" />
          </div>
          <div className="intro-drift mid">
            <span className="intro-stars mid" />
          </div>
          <div className="intro-drift near">
            <span className="intro-stars near" />
          </div>
        </div>
        <div className="intro-grid" />

        <div className="intro-hud">
          <span className="hud-corner tl" />
          <span className="hud-corner tr" />
          <span className="hud-corner bl" />
          <span className="hud-corner br" />
          <div className="hud-block hud-tl">
            <span>SYN-01 · DEEP FIELD ARRAY</span>
            <span className="hud-dim">RA 05h 35m 17s · DEC −05° 23′ 28″</span>
          </div>
          <div className="hud-block hud-tr">
            <span className="hud-clock">MET T+00:00:</span>
            <span className="hud-dim">EPOCH J2000</span>
          </div>
          <div className="hud-block hud-bl">
            <span className="hud-dim">RX 1420.405 MHz · HYDROGEN LINE</span>
          </div>
          <div className="hud-block hud-status">
            <span className="hud-line s1">● ACQUIRING SIGNAL</span>
            <span className="hud-line s2">◆ TARGET LOCKED</span>
            <span className="hud-line s3">▲ UPLINK ESTABLISHED</span>
            <span className="hud-progress" />
          </div>
        </div>

        <div className="intro-center">
          <div className="intro-target">
            <div className="intro-glow" />
            <div className="intro-pulse" />
            <svg className="intro-orbit" viewBox="-110 -110 220 220">
              <ellipse id="intro-orbit-path" rx="104" ry="34" transform="rotate(-16)" />
              <circle r="2.2" className="intro-particle">
                <animateMotion dur="7s" repeatCount="indefinite" rotate="auto">
                  <mpath href="#intro-orbit-path" />
                </animateMotion>
              </circle>
              <circle r="1.4" className="intro-particle dim">
                <animateMotion dur="7s" begin="-3.5s" repeatCount="indefinite">
                  <mpath href="#intro-orbit-path" />
                </animateMotion>
              </circle>
            </svg>
            {/* The real brand artwork, reused from the masthead. */}
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img className="intro-art" src="/brand/emblem-480.jpg" width={480} height={480} alt="" fetchPriority="high" />
            <div className="intro-reticle">
              <span className="ret tl" />
              <span className="ret tr" />
              <span className="ret bl" />
              <span className="ret br" />
              <span className="ret-cross" />
            </div>
          </div>
          <div className="intro-brand">
            <p className="intro-name">Synergy</p>
            <p className="intro-tagline">A universe of ideas.</p>
          </div>
        </div>
      </div>

      <button type="button" className="intro-skip" data-intro-skip="">
        Skip intro <span aria-hidden="true">→</span>
      </button>
    </div>
  );
}

/** Runs before first paint (inlined in <head>). Plays the intro once per
 *  browser session (session cookie, plus sessionStorage), never with
 *  prefers-reduced-motion, and always ends it: after the 6 s sequence, on
 *  Skip or Escape, or immediately if anything here throws. It needs no
 *  React, so Skip works before hydration. */
export const introInitScript = `(function(){var d=document.documentElement,K="synergy_intro";
function seen(){try{if(sessionStorage.getItem(K))return true}catch(e){}return document.cookie.indexOf(K+"=1")>-1}
function mark(){try{sessionStorage.setItem(K,"1")}catch(e){}try{document.cookie=K+"=1; path=/; SameSite=Lax"}catch(e){}}
try{if(seen()||!window.matchMedia||matchMedia("(prefers-reduced-motion: reduce)").matches){d.dataset.intro="done";return}
mark();d.dataset.intro="play";var timer;
function end(){if(d.dataset.intro!=="play")return;clearTimeout(timer);d.dataset.intro="leaving";
document.removeEventListener("click",onClick,true);document.removeEventListener("keydown",onKey,true);
setTimeout(function(){d.dataset.intro="done";var a=document.activeElement;if(a&&a.closest&&a.closest("[data-intro-root]"))a.blur()},450)}
function onClick(e){var t=e.target;if(t&&t.closest&&t.closest("[data-intro-skip]")){e.preventDefault();end()}}
function onKey(e){if(e.key==="Escape")end()}
document.addEventListener("click",onClick,true);document.addEventListener("keydown",onKey,true);
document.addEventListener("DOMContentLoaded",function(){var b=document.querySelector("[data-intro-skip]");if(b&&d.dataset.intro==="play")b.focus({preventScroll:true})});
timer=setTimeout(end,6000)}catch(e){d.dataset.intro="done"}})();`;
