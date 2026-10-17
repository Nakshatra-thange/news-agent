import "./Intro.css";
import { BallTree, MushroomLamp, RetroTV, RobotHead, TuneBox } from "./RetroObjects";

/** The intro: pastel blobs pop open, the name bounces in and a few retro
 *  objects tumble around it, then the feed.
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
        <span className="intro-blob b1" />
        <span className="intro-blob b2" />
        <span className="intro-blob b3" />
        <span className="intro-blob b4" />
        <div className="intro-center">
          <RetroTV id="intro-tv" className="intro-obj o1" />
          <RobotHead id="intro-bot" className="intro-obj o2" />
          <MushroomLamp id="intro-lamp" className="intro-obj o3" />
          <TuneBox id="intro-box" className="intro-obj o4" />
          <BallTree id="intro-tree" className="intro-obj o5" />
          <p className="intro-name">
            {"Synergy".split("").map((ch, i) => (
              <span key={i} style={{ animationDelay: `${0.75 + i * 0.06}s` }}>
                {ch}
              </span>
            ))}
          </p>
          <p className="intro-tagline">AI news, one card at a time.</p>
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
 *  prefers-reduced-motion, and always ends it: after the 3 s sequence, on
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
timer=setTimeout(end,3000)}catch(e){d.dataset.intro="done"}})();`;
