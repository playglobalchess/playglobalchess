// Codec Android parity — instrumented test (SPEC-S2-PROTOCOL §8, PLAN-CODEC
// §4.3, NFR-10, ADR-9).
//
// Runs the SAME parity assertions as platform/mobile/KotlinCodecParityTest.kt,
// but as a JUnit instrumented test so it executes on an Android emulator/device
// via `./gradlew connectedAndroidTest` — where the aarch64 .so loads (it cannot
// on a host JVM). The codec .so + the generated UniFFI Kotlin bindings are
// placed by platform/mobile/build_codec_mobile.sh.
//
// Frames mirror protocol/codec-ffi/tests/codec_parity_oracle.rs and are a subset
// of protocol/vectors/vectors.json — change in lockstep (the parity contract).

package com.playglobalchess.codecparity

import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertThrows
import org.junit.Test
import org.junit.runner.RunWith
import uniffi.pgc_protocol_ffi.decodeClient
import uniffi.pgc_protocol_ffi.decodeEvent
import uniffi.pgc_protocol_ffi.decodeServer
import uniffi.pgc_protocol_ffi.encodeClient
import uniffi.pgc_protocol_ffi.encodeEvent
import uniffi.pgc_protocol_ffi.encodeServer

@RunWith(AndroidJUnit4::class)
class CodecParityTest {
    private val clientMovePromoHex =
        "01032466343761633130622d353863632d343337322d613536372d30653032623263336434373967056537653871"
    private val serverGameEndHex =
        "01852466343761633130622d353863632d343337322d613536372d3065303262326333643437390202000a021817"
    private val eventRatingAppliedHex =
        "2967616d652f66343761633130622d353863632d343337322d613536372d3065303262326333643437390c0c008080cea6a5320e626c69747a2d7374616e646172640200b817d0173c8fc2753d01b817a0173c8fc2753d"

    private fun fromHex(h: String): ByteArray =
        ByteArray(h.length / 2) {
            ((h[it * 2].digitToInt(16) shl 4) or h[it * 2 + 1].digitToInt(16)).toByte()
        }

    @Test
    fun clientDecodeReencodeIdentity() {
        val b = fromHex(clientMovePromoHex)
        assertArrayEquals(b, encodeClient(decodeClient(b)))
    }

    @Test
    fun serverDecodeReencodeIdentity() {
        val b = fromHex(serverGameEndHex)
        assertArrayEquals(b, encodeServer(decodeServer(b)))
    }

    @Test
    fun eventDecodeReencodeIdentity() {
        val b = fromHex(eventRatingAppliedHex)
        assertArrayEquals(b, encodeEvent(decodeEvent(b)))
    }

    @Test
    fun malformedFrameThrowsNotCrash() {
        assertThrows(Exception::class.java) { decodeClient(ByteArray(0)) }
    }
}
