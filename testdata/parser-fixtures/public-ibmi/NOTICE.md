# Public Fixture Attribution

These excerpts are third-party material, not relicensed under Tirion's Apache-2.0
license. Their project attribution is recorded in fixture headers and the corpus
README. Preserve the following upstream license notices with these files.

The original import did not record source revisions. License blob identifiers
below pin the notice text retrieved on 2026-09-09; they are not claimed as the
source revision of the excerpts.

## Source Mapping

Compared on 2026-09-09 against the immutable upstream blobs below. These fixtures
are adapted excerpts, not full-file reproductions. Comparison ignored blank lines,
leading/trailing whitespace, and the local attribution comment; matching counts
are line membership, not proof of the original import revision or a contiguous copy.

| Fixture | Upstream path and blob | Comparison |
| --- | --- | --- |
| public_package.clle | [src/clsrc/PACKAGE.clle](https://api.github.com/repos/ScottKlement/httpapi/git/blobs/497d0fd1a5d38601a1172e01ec6bdf630a462bb4) | 13/15 lines match; local CRTCLPGM and CALL statements differ |
| public_configs.dspf | [src/ddssrc/CONFIGS.dspf](https://api.github.com/repos/ScottKlement/httpapi/git/blobs/576d0555313f47fe0c5868afa53600252d6dabe3) | 21/21 lines match |
| public_configr4.rpgle | [src/rpglesrc/CONFIGR4.rpgle](https://api.github.com/repos/ScottKlement/httpapi/git/blobs/c569a679e00a31248c80917e8b109bccfb81c9c1) | 30/31 lines match; local QCMDEXC call differs |
| public_qshcallc.clle | [QSHCALLC.CLLE](https://api.github.com/repos/richardschoen/QshOni/git/blobs/4d5473d2df3cc7a4e40d7aa9cc1bf3d4b4159fee) | 17/19 lines match; local PGM and CRTDTAARA statements differ |
| public_qshonisrv.rpgle | [QSHONISRV.RPGLE](https://api.github.com/repos/richardschoen/QshOni/git/blobs/777d146ccc4be334e40e6fa902241cce579037f5) | 16/47 lines match; substantially simplified procedure declarations and bodies |

The notices below apply to the upstream-derived material, including these
adaptations. Do not describe the adapted fixtures as unmodified upstream files.

## ScottKlement/httpapi

Source: https://github.com/ScottKlement/httpapi

License: BSD-2-Clause; upstream file LICENSE; blob 0ab21cd5b1077ab330d4f400ebf495c6710df272.

Files: public_configr4.rpgle, public_configs.dspf, public_package.clle.

```text
Copyright (c) 2001-2025 Scott C. Klement, Thomas Raddatz
All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions
are met:
1. Redistributions of source code must retain the above copyright
   notice, this list of conditions and the following disclaimer.
2. Redistributions in binary form must reproduce the above copyright
   notice, this list of conditions and the following disclaimer in the
   documentation and/or other materials provided with the distribution.

THIS SOFTWARE IS PROVIDED BY THE AUTHOR AND CONTRIBUTORS ''AS IS'' AND
ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
ARE DISCLAIMED.  IN NO EVENT SHALL THE AUTHOR OR CONTRIBUTORS BE LIABLE
FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS
OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT
LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY
OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF
SUCH DAMAGE.
```

## richardschoen/QshOni

Source: https://github.com/richardschoen/QshOni

License: MIT; upstream file license.md; blob c8b897ab3af5eeea1465e7f1666eefcd724bf5fb.

Files: public_qshonisrv.rpgle, public_qshcallc.clle.

```text
MIT License

Copyright (c) 2019-2024 Richard Schoen

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
