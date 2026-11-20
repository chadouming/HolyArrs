---
title: Subtitles
description: Pick your subtitle languages, see which movies and episodes are missing one, and fix or replace subtitles.
level: Setup
sidebar: { order: 3 }
---

Pick your subtitle languages once. HolyArrs reads what your files already have and lists what is missing.

Only staff see subtitles. Changing settings needs the **Change settings** permission. Music has no subtitles.

:::note
Subtitle sites and subtitle search are held back in the first release. They stay hidden unless the `subtitle_sites` switch is on, see [Held-back features](../run/updates.md#held-back-features). Languages and the Missing subtitles list work without it.
:::

## Set your languages

1. Open **Settings > Subtitles**.
2. Under the default languages, add each language you want, such as English and Spanish.
3. For each language, set **Hearing-impaired** and **Forced only** (see [Languages](#languages)).
4. Click **Save**. You should see **Library > Missing subtitles** fill in. HolyArrs reads every file you have and asks no site.
5. If Subtitle sites are on for you, click **Add a Subtitle site**, pick the site, fill in **Your account** if it asks, and save. You should see the result of a test search right away.

Files brought in with **Import a library** are read the same way.

## See what is missing

Open **Library > Missing subtitles**. Each row is a movie or episode that lacks a subtitle, with the reason (not looked for yet, not found yet, or nothing over the Pass mark yet) and the next try. A title removed from the Library is left out, even when its files stay on disk.

- Filter by language, by movies or episodes, and by title. The page address keeps your filters, so you can bookmark a view.
- **Search all** searches every row in the filtered list and shows progress.
- **Search** on a row searches that one file.

### The Subtitles page

**Subtitles**, under Activity in the side menu, shows the same list with what can be done about each row. It is for people who look after the Library.

- **Missing** counts the wanted subtitles that are not there, and the files they belong to.
- **Need speech to text** counts the files with no subtitle that can be read, in any language. Nothing can be translated for them, so a site has to have the subtitle or it is [made from the audio](#subtitles-from-the-audio). The line under the number says whether that happens by itself, and if not, why.
- **Can be translated** counts the missing subtitles on files that already have one in another language.
- **Working now** lists the subtitle jobs that run or wait: searches, translations, subtitles from the audio and model downloads. A translation and a subtitle from the audio show how far they are.
- **Subtitle sites** shows each site's state and how many of its downloads for today are used.
- **Done** lists the newest subtitles HolyArrs saved that are still beside their videos, and says how each was made: downloaded from a site, translated, or made from the audio (speech to text). The counts beside the heading cover all of them, not only the ones listed.

Click a number to narrow the list to those rows. Each row says what happens next, and has **Search** and, for a file with nothing to read, **Make from audio**.

## Fix one movie or episode

A movie page has a **Subtitles** card. A show page has one for the whole show. Each episode with a file has a **Subtitles** button on its row.

| Control | What it does |
|---|---|
| **Languages** | Shows what this title wants and where it comes from: the default languages, the show, or its own. **Change** switches to its own list. An empty own list means no subtitles. |
| **Look for subtitles** | Searches for what is missing now. |
| **Choose a subtitle** | Lists every result from every site with its score out of 100 and what matched. **Choose** saves it, whatever its score. |
| **Sync fix** | For subtitles HolyArrs saved. Shifts every line by up to 10 minutes, changes the frame rate (such as 25 to 23.976), or both. The old file goes to the holding folder. |
| **Make from audio** | On a file with no text subtitle at all, when the speech models are set up. HolyArrs listens to the video and writes a subtitle in the language that is spoken. See [Subtitles from the audio](#subtitles-from-the-audio). |
| **Translate** | Next to a missing language, when a translator is set up. Pick a text subtitle beside the video or inside it, and your translator makes the missing one. |
| **Turn down** | For subtitles HolyArrs saved. The file goes to the holding folder, that subtitle is never picked again for this file, and HolyArrs looks for another. |

A subtitle no person made carries a label: **Translated**, **Partly translated** (some lines kept the original language), **AI-translated** or **Machine-translated** (a site's own machine-made file), or **Transcribed** (made from the video's audio). Picture subtitles inside the video are marked **Picture** and can't be translated.

Each subtitle shows its language, kind (normal, hearing-impaired or forced), source (inside the video, beside it, a site, or chosen by hand), score and file name. Everything that happens shows in the title's **History** under Subtitles, and [Alerts](../users/alerts.md) can tell you too.

## Choices

### Languages

| Option | Choices |
|---|---|
| **Hearing-impaired** | Normal (a hearing-impaired subtitle also counts), Hearing-impaired only (waits for one with sound descriptions), or Either. |
| **Forced only** | Only the lines for foreign speech and signs. |
| **Translate from** | Shows once a translator is set up. When no subtitle in this language is found, your translator makes one from the language you pick here. **Any language the video has** takes whatever text subtitle the video has, beside it or inside it, in any other language; English first when there is one. Sites are not asked for a source then. |

Up to 10 languages. A language can appear twice only if one is full and the other forced only. With no languages set, HolyArrs looks for nothing.

### Where subtitles are saved

Next to the video: the video's name, the language, then `.hi` (or `.cc`, `.sdh`) or `.forced`, then the format.

```
Movie (2020).en.srt
Movie (2020).es.hi.srt
Movie (2020).fr.forced.ass
```

- HolyArrs never overwrites a subtitle file it did not save. That file counts as there.
- A subtitle HolyArrs replaces goes to the holding folder first.
- **Rename files** moves subtitles with their video. If the new name is taken, that subtitle stays and says why.
- Deleting a title with its files sends its subtitles to the holding folder too.

Portuguese (Brazil) has its own files, `Movie (2020).pt-BR.srt`. Older `.pt` files stay where they are, so both can sit next to the video.

### Translate subtitles

In **Settings > Subtitles > Translate subtitles**, pick your own translator:

| Translator | What to fill in |
|---|---|
| **AI service** | The address of any service that speaks the OpenAI chat API (OpenRouter, OpenAI, Ollama, LM Studio), the model, and a key if it needs one. **Test** lists the models it offers. A local one keeps everything at home. |
| **Lingarr** | The address of your Lingarr server. |
| **Built in** | A model that runs inside the HolyArrs container. Pick a model, and a graphics card if the container has one. Nothing leaves your server and there is no key. Needs the `ai` image, see [Built in](#built-in). |

The key is kept encrypted and never shown again. Leave the box empty to keep it, or tick **Remove the saved key**.

**Translate subtitles inside the video by themselves** lets the timed search translate text tracks inside the video. Off, those are translated only when you click **Translate**. Picture tracks are always skipped.

#### Built in

The built-in translator needs the HolyArrs image that holds the model runner. The usual image does not hold it, and there **Built in** is greyed out. Build it from the source with the `ai` target:

```
docker build -f deploy/Dockerfile --target ai -t holyarrs:ai .
```

It is the same HolyArrs with the same settings and `/config` folder, on a larger image (about 420 MB). It is built for amd64 and arm64. Like the usual image it holds no shell and no system tools: only HolyArrs, the runners, and the files they load.

A good part of that size is the graphics drivers. The image holds Mesa's Vulkan drivers for Intel, AMD and NVIDIA (nouveau) cards. AMD's needs about 150 MB of its own. To leave out the cards you do not have, name the ones to keep (`intel`, `intel_hasvk` for Intel before Gen 9, `radeon`, `nouveau`):

```
docker build -f deploy/Dockerfile --target ai --build-arg VULKAN_DRIVERS="intel" -t holyarrs:ai .
```

| Option | What it does |
|---|---|
| **Model** | **Download** fetches the model into `/config/models` and checks it. A download that stops goes on from where it was. You can also put your own `.gguf` file in that folder; it shows as **your own file**. **Use** picks a model, **Delete** removes its file. |
| **Graphics card** | Runs the model on a card, which is much faster than the CPU. Cards show here when the container is given one, with `--device /dev/dri` on Docker or Podman. **None** keeps the model on the CPU. With more than one card, **All cards** runs the model on each of them at once. |
| **CPU threads** | Empty for the default, at most 8. More threads rarely help when the server is busy with other work. |

- Two models are offered. Hy-MT2 1.8B (about 1.8 GB on disk) is made for translating and is the quicker one. Gemma 4 E4B (about 4.3 GB) is a general model, a little slower and slower to start.
- On a graphics card a file is cut into parts that are translated at the same time, eight on each card.
- While it translates, Hy-MT2 1.8B holds about 3 GB of memory on the CPU, and 4 to 6 GB with a graphics card (plus the card's own memory).
- The model runs only while subtitles are being translated and stops five minutes after the last one.
- On a graphics card a film takes minutes. On a busy CPU it can take hours.
- If the model needs more memory than is free, the translation fails with a line that says so, and nothing else in HolyArrs is touched.

### Subtitles from the audio

When a file has no subtitle at all, inside the video or beside it, HolyArrs can make one by listening to the video. It runs on your own server and needs the `ai` image, like the built-in translator.

In **Settings > Subtitles > Subtitles from the audio**:

| Option | What it does |
|---|---|
| **Make subtitles from the audio when a file has none** | Off by default. On, a search that saves nothing for a file with no text subtitle in any language asks for a transcript. It does not wait for the sites: it also runs when their downloads for the day are used up, when they are paused, or when none is set up. |
| **Models** | Two downloads: Whisper large-v3-turbo (about 1.6 GB) and a small voice-detection model. Both are needed. |

- **Which audio:** the track tagged with a language you want, in the order you want them; else the title's original language; else the first track. HolyArrs then listens to three samples to hear what is really spoken, so a wrong tag does not give a subtitle in the wrong language. If it cannot tell, nothing is saved.
- **Which language:** the subtitle is in the language that is spoken. Your other languages are made from it by the translator, where a language's **Translate from** names that language or is **Any language the video has**.
- **Replaced later:** a subtitle a person made replaces a transcript when a site has one, the way it replaces a translated file.
- **By hand:** **Make from audio** on a title's subtitle panel does one file, whatever the switch says.
- **One at a time.** It uses the graphics card chosen for the built-in translator, or the CPU, which is much slower.
- **Not tried again by itself** when no speech was found or the language could not be told. A failure that may pass is tried again a day later, three times at most.

#### Names and terms

A show's own words (character names, places, attack names, invented terms) are what a transcript most often gets wrong. On a title's subtitle panel, **Names and terms** holds what the transcriber is told:

- **Your own**, one a line. These always come first.
- **Gathered from this title's subtitles:** names and terms taken from the subtitles the title already has in that language, ranked by how many of them use each. You can leave any of them out. Nothing is looked up outside your server.

There is room for about a hundred terms.

#### What to expect

- On clear speech most lines are right. On fast, slang-heavy dialogue about four words in five matched a subtitle made by a person.
- Names it was never told are often misspelled.
- After a long stretch of music or silence, a few lines can show early.
- A file with several audio tracks in the language you want gets the first of them transcribed, which may be a commentary.
- Not measured yet: how much gathered terms help, and how good a transcript of Japanese audio and its translation are.

Each subtitle site has **Machine-made subtitles**. Off by default. On, a site's AI or machine-translated files are used only when no file made by a person exists, and a person's file replaces it later.

How scoring works, when HolyArrs searches, and the sites it uses are in [Subtitle scoring and sites](subtitle-scoring.md).

## If it does not work

**Symptom:** nothing is listed as missing.
**Cause:** no language was saved, or the title has an empty own list.
**Fix:** add a language and click **Save**.

**Symptom:** **Built in** is greyed out under Translate subtitles.
**Cause:** this image does not hold the model runner.
**Fix:** build and run the `ai` image, see [Built in](#built-in).

**Symptom:** **Make from audio** is not there.
**Cause:** the file already has a text subtitle, this is not the `ai` image, or the two models are not downloaded.
**Fix:** check **Settings > Subtitles > Subtitles from the audio**.

**Symptom:** no transcript was made for a file.
**Cause:** no speech was found, or the spoken language could not be told. The title's **History** says which.
**Fix:** use **Make from audio** to try again by hand.

**Symptom:** no graphics card is listed for the built-in translator.
**Cause:** the container was not given one.
**Fix:** add `--device /dev/dri` to the container and start it again.

**Symptom:** you can't see Subtitle sites or **Look for subtitles**.
**Cause:** they are held back, see [Held-back features](../run/updates.md#held-back-features).

**Symptom:** subtitles are out of time.
**Fix:** use **Sync fix**, or raise the Pass mark so only closer matches are saved. HolyArrs does not listen to the sound to line subtitles up.

**Symptom:** a subtitle is wrong.
**Fix:** use **Turn down**.

**Symptom:** an existing subtitle was not replaced.
**Cause:** HolyArrs never overwrites subtitle files it did not save.

## For experts

<details class="ha-more"><summary>Settings reference</summary>

| Setting | Default | Range |
|---|---|---|
| Default languages | none | up to 10 |
| **Hearing-impaired** file ending | `.hi` | `.hi`, `.cc`, `.sdh` |
| **Count subtitles inside the video** | On | On or Off |
| **Leave out picture subtitles** | Off | On or Off |

Scoring, schedule and Sync fix settings are in [Subtitle scoring and sites](subtitle-scoring.md).

</details>

<details class="ha-more"><summary>API</summary>

Every subtitle route is in `http://your-server:6262/api/v1/openapi.json`, under the Subtitles tag. Searches answer with the ids of their Jobs, so a script can follow them. See [API routes](../reference/api-routes.md).

</details>

## Related

- [Subtitle scoring and sites](subtitle-scoring.md)
- [Audio versions](audio-versions.md)
- [Held-back features](../run/updates.md#held-back-features)
