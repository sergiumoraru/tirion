     * excerpted from Scott Klement httpapi CONFIGR4.rpgle
      /copy VERSION

     H DFTACTGRP(*NO)

     FCONFIGS   CF   E             WORKSTN sfile(CONFIG2S: WKRRN2)
     F                                     sfile(CONFIG3S: WKRRN3)
     FSRCPF     UF   F  112        DISK    USROPN

      /copy HTTPAPI_H

     D QCMDEXC         PR                  ExtPgm('QCMDEXC')
     D   command                    200A   const
     D   length                      15P 5 const

     c                   callp     QCMDEXC('OVRDBF FILE(SRCPF) '
     c                                    +      ' TOFILE(QRPGLESRC) '
     c                                    +      ' MBR(LICENSE)'
     c                                    : 200)

     c                   open      SRCPF
     c                   read(N)   SRCPF
     c                   eval      wkRRN2 = wkRRN2 + 1
     c                   eval      scLine = wkLine
     c                   write     CONFIG2S
     c                   close     SRCPF
     c                   callp     QCMDEXC('DLTOVR FILE(SRCPF)' : 200)

     c                   write     CONFIG2F
     c                   exfmt     CONFIG2C
     c                   exfmt     CONFIGS1
     c                   write     CONFIG3F
     c                   exfmt     CONFIG3C
     c                   exfmt     CONFIGS4
     c                   exfmt     CONFIGS5

     c                   if        scSrcLib <> '*LIBL'
     c                   callp(e)  QCMDEXC('CHKOBJ OBJ(' + %trim(scSrcLib)
     c                                    + ') OBJTYPE(*LIB)': 200)
     c                   endif
