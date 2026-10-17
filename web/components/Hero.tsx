import { showcase } from "./RetroObjects";

/** The front-page hero: the name, a one-line pitch and a showcase of retro
 *  objects. Purely presentational. */
export function Hero() {
  return (
    <section className="hero" aria-labelledby="hero-title">
      <div className="hero-copy">
        <h1 id="hero-title" className="hero-title">
          Synergy
        </h1>
        <p className="hero-pitch">
          Bite-sized AI news from Hacker News, arXiv and GitHub. Every story is a flashcard: read the headline, flip
          it for the gist.
        </p>
        <a href="#feed" className="button primary">
          Start flipping
        </a>
        <span className="sticker" aria-hidden="true">
          fresh
          <br />
          daily!
        </span>
      </div>
      <div className="hero-show">
        <div className="showcase" aria-hidden="true">
          {showcase.map((Obj, i) => (
            <Obj key={i} id={`hero-${i}`} className="showcase-item" />
          ))}
        </div>
      </div>
    </section>
  );
}
