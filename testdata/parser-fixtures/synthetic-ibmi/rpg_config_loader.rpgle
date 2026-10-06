     /copy VERSION

     H DFTACTGRP(*NO)
     FSRCPF     UF   F  112        DISK    USROPN

     D QCMDEXC         PR                  ExtPgm('QCMDEXC')
     D   command                    200A   const
     D   length                      15P 5 const

     c                   eval      *inlr = *on
     c                   callp(e)  QCMDEXC('OVRDBF FILE(SRCPF)': 200)
     c                   open      SRCPF
     c                   read(N)   SRCPF
     c                   dow       not %eof(SRCPF)
     c                   write     CONFIG2S
     c                   read(N)   SRCPF
     c                   enddo
     c                   close     SRCPF
